use actix_cors::Cors;
use actix_files::Files;
use actix_web::{
    get, post, web, App, HttpResponse, HttpServer, Responder,
    middleware::Logger,
    HttpRequest,
};
use serde::{Deserialize, Serialize};
use std::{env, path::PathBuf, time::Duration, time::Instant};
use std::process::Command;
use std::str;
use futures::future::join_all;

// Equivalent struct definitions
#[derive(Debug, Deserialize)]
struct ChatRequest {
    message: String,
}

#[derive(Debug, Serialize)]
struct ChatResponse {
    content: String,
}

#[derive(Debug, Deserialize)]
struct LLMResponse {
    choices: Vec<Choice>,
}

#[derive(Debug, Deserialize)]
struct Choice {
    message: Message,
}

#[derive(Debug, Deserialize)]
struct Message {
    content: String,
}

#[derive(Debug, Deserialize)]
struct LoadTestRequest {
    requests: u64,     // Total number of requests
    concurrency: u64,  // Concurrent users
}

#[derive(Debug, Serialize)]
struct ResponseTime {
    min: Duration,
    max: Duration,
    mean: Duration,
    p95: Duration,
    p99: Duration,
}

#[derive(Debug, Serialize)]
struct ErrorDetail {
    name: String,
    count: i64,
    error_type: String,
}

#[derive(Debug, Serialize)]
struct LoadTestResult {
    total_requests: u64,
    successful_requests: u64,
    failed_requests: u64,
    total_duration_ms: u64,
    average_response_ms: f64,
    requests_per_second: f64,
}

// API handlers
#[get("/api")]
async fn hello() -> impl Responder {
    HttpResponse::Ok().json(serde_json::json!({
        "message": "Welcome to the LLM Chat API"
    }))
}

#[post("/api/chat")]
async fn chat_with_llm(
    req: web::Json<ChatRequest>,
    client: web::Data<reqwest::Client>,
    app_state: web::Data<AppState>,
) -> actix_web::Result<HttpResponse> {
    log::info!("Received message: {}", req.message);

    let payload = serde_json::json!({
        "messages": [
            {
                "role": "user",
                "content": req.message
            }
        ]
    });

    log::info!("Sending request to LLM endpoint: {}", app_state.llm_endpoint);
    
    let response = client
        .post(&app_state.llm_endpoint)
        .header("Authorization", format!("Bearer {}", app_state.api_key))
        .json(&payload)
        .send()
        .await
        .map_err(|e| {
            log::error!("Failed to send request: {}", e);
            actix_web::error::ErrorInternalServerError("Failed to send request to LLM")
        })?;

    // Get status before consuming the response
    let status = response.status();
    
    if !status.is_success() {
        let error_body = response.text().await.unwrap_or_default();
        log::error!(
            "HTTP error occurred. Status: {}, Body: {}",
            status,
            error_body
        );
        return Ok(HttpResponse::InternalServerError().json(serde_json::json!({
            "error": "Error from LLM endpoint"
        })));
    }

    log::info!("Received response from LLM");

    let llm_resp: LLMResponse = response.json().await.map_err(|e| {
        log::error!("Failed to decode response: {}", e);
        actix_web::error::ErrorInternalServerError("Invalid response from LLM endpoint")
    })?;

    let content = llm_resp
        .choices
        .first()
        .and_then(|choice| Some(choice.message.content.clone()))
        .ok_or_else(|| {
            log::error!("Invalid response structure from LLM");
            actix_web::error::ErrorInternalServerError("Invalid response structure from LLM endpoint")
        })?;

    Ok(HttpResponse::Ok().json(ChatResponse { content }))
}

#[get("/api/loadtest")]
async fn handle_load_test(
    query: web::Query<LoadTestRequest>,
    client: web::Data<reqwest::Client>,
) -> actix_web::Result<HttpResponse> {
    log::info!("Starting load test with {} requests, {} concurrent users", 
        query.requests, query.concurrency);

    let start_time = Instant::now();
    let mut durations = Vec::new();
    let mut success_count = 0;
    let mut failure_count = 0;

    // Create batches of concurrent requests
    for batch in (0..query.requests).collect::<Vec<_>>().chunks(query.concurrency as usize) {
        let requests = batch.iter().map(|_| {
            let client = client.clone();
            async move {
                let request_start = Instant::now();
                let result = client
                    .get("http://localhost:8000/api")
                    .send()
                    .await;
                let duration = request_start.elapsed();
                
                match result {
                    Ok(_) => {
                        success_count += 1;
                        Some(duration.as_millis() as u64)
                    },
                    Err(e) => {
                        log::error!("Request failed: {}", e);
                        failure_count += 1;
                        None
                    }
                }
            }
        });

        // Execute concurrent batch
        let batch_results = join_all(requests).await;
        durations.extend(batch_results.into_iter().flatten());
    }

    let total_duration = start_time.elapsed();
    let avg_response = durations.iter().sum::<u64>() as f64 / durations.len() as f64;
    let requests_per_sec = query.requests as f64 / total_duration.as_secs_f64();

    let result = LoadTestResult {
        total_requests: query.requests,
        successful_requests: success_count,
        failed_requests: failure_count,
        total_duration_ms: total_duration.as_millis() as u64,
        average_response_ms: avg_response,
        requests_per_second: requests_per_sec,
    };

    log::info!("Load test completed: {:?}", result);
    Ok(HttpResponse::Ok().json(result))
}

// Helper function to check if file exists
fn file_exists(path: &PathBuf) -> bool {
    path.exists()
}

struct AppState {
    llm_endpoint: String,
    api_key: String,
}

#[actix_web::main]
async fn main() -> std::io::Result<()> {
    // Initialize environment variables and logging
    dotenv::dotenv().ok();
    env_logger::init();

    // Load environment variables
    let databricks_host = env::var("DATABRICKS_HOST")
        .expect("DATABRICKS_HOST must be set");
    let llm_endpoint = env::var("SERVING_ENDPOINT_NAME")
        .expect("SERVING_ENDPOINT_NAME must be set");
    let api_key = env::var("DATABRICKS_TOKEN")
        .expect("DATABRICKS_TOKEN must be set");
    let llm_endpoint = format!("https://{}/serving-endpoints/{}/invocations", databricks_host, llm_endpoint);
    let app_state = web::Data::new(AppState {
        llm_endpoint,
        api_key,
    });

    let client = reqwest::Client::new();
    
    // Get the current directory (where client/build should be)
    let current_dir = env::current_dir()?
        .to_path_buf();
    
    let static_path = current_dir.join("client").join("build");
    
    // Debug logging
    log::info!("Static files path: {:?}", static_path);
    if !static_path.exists() {
        log::warn!("Static files directory does not exist at: {:?}", static_path);
    }

    log::info!("Starting the Rust server...");

    HttpServer::new(move || {
        let cors = Cors::default()
            .allow_any_origin()
            .allow_any_method()
            .allow_any_header()
            .supports_credentials()
            .max_age(43200);

        let app = App::new()
            .wrap(Logger::default())
            .wrap(cors)
            .app_data(app_state.clone())
            .app_data(web::Data::new(client.clone()))
            .service(hello)
            .service(chat_with_llm)
            .service(handle_load_test);

        // Only add static file handlers if the directory exists
        if static_path.exists() {
            app.service(Files::new("/static", static_path.join("static")))
                .service(Files::new("/", &static_path).index_file("index.html"))
        } else {
            app
        }
    })
    .bind(("127.0.0.1", 8000))?
    .run()
    .await
} 