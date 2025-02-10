package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// ChatRequest represents the incoming chat request
type ChatRequest struct {
	Message string `json:"message"`
}

// ChatResponse represents the outgoing chat response
type ChatResponse struct {
	Content string `json:"content"`
}

// LLMResponse represents the response from the LLM endpoint
type LLMResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// LoadTestRequest represents the incoming load test configuration
type LoadTestRequest struct {
	Users     int `form:"users" binding:"required,gt=0"`
	SpawnRate int `form:"spawn_rate" binding:"required,gt=0"`
	TestTime  int `form:"test_time" binding:"required,gt=0"`
}

// LoadTestResponse represents the load test results
type LoadTestResponse struct {
	TestDuration       int     `json:"test_duration"`
	TotalRequests      int64   `json:"total_requests"`
	SuccessfulRequests int64   `json:"successful_requests"`
	FailedRequests     int64   `json:"failed_requests"`
	RequestsPerSecond  float64 `json:"requests_per_second"`
	ConcurrentUsers    int     `json:"concurrent_users"`
	ResponseTime       struct {
		Min  time.Duration `json:"min"`
		Max  time.Duration `json:"max"`
		Mean time.Duration `json:"mean"`
		P95  time.Duration `json:"p95"`
		P99  time.Duration `json:"p99"`
	} `json:"response_time"`
	Errors []ErrorDetail `json:"errors"`
}

type ErrorDetail struct {
	Name      string `json:"name"`
	Count     int64  `json:"count"`
	ErrorType string `json:"error_type"`
}

var (
	llmEndpoint string
	apiKey      string
)

func init() {
	// Load environment variables
	llmEndpoint = os.Getenv("SERVING_ENDPOINT_NAME")
	apiKey = os.Getenv("DATABRICKS_TOKEN")

	if llmEndpoint == "" || apiKey == "" {
		log.Fatal("Missing required environment variables")
	}
}

func StartGoServer() {
	r := gin.Default()

	// Debug: Print current working directory and static file path
	currentDir, _ := os.Getwd()
	staticPath := filepath.Join(currentDir, "client/build")

	// CORS middleware configuration first
	config := cors.Config{
		AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Length", "Content-Type", "Authorization"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}
	r.Use(cors.New(config))

	// API routes first
	r.GET("/api", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "Welcome to the LLM Chat API"})
	})

	r.OPTIONS("/api/chat", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	r.POST("/api/chat", chatWithLLM)

	// Add the load test endpoint
	r.GET("/api/load-test", handleLoadTest)

	//Static file serving last
	r.Static("/static", filepath.Join(staticPath, "static"))
	r.NoRoute(func(c *gin.Context) {
		indexPath := filepath.Join(staticPath, "index.html")
		if fileExists(indexPath) {
			c.File(indexPath)
		} else {
			log.Printf("Index file not found at: %s", indexPath)
			c.String(http.StatusNotFound, "File not found")
		}
	})

	log.Println("Starting the Go server...")
	if err := r.Run(fmt.Sprintf(":%s", os.Getenv("DATABRICKS_APP_PORT"))); err != nil {
		log.Fatal(err)
	}
}

func chatWithLLM(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("Received message: %s", req.Message)

	payload := map[string]interface{}{
		"messages": []map[string]string{
			{"role": "user", "content": req.Message},
		},
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create payload"})
		return
	}

	log.Printf("Payload: %s", string(jsonPayload))

	client := &http.Client{}
	requestURL := fmt.Sprintf("https://%s/serving-endpoints/%s/invocations", os.Getenv("DATABRICKS_HOST"), llmEndpoint)
	httpReq, err := http.NewRequest("POST", requestURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create request"})
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

	log.Printf("Sending request to LLM endpoint: %s", llmEndpoint)
	resp, err := client.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to send request to LLM"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		log.Printf("HTTP error occurred. Status: %d, Body: %s", resp.StatusCode, string(body))
		c.JSON(resp.StatusCode, gin.H{"error": "Error from LLM endpoint"})
		return
	}

	log.Println("Received response from LLM")

	var llmResp LLMResponse
	if err := json.NewDecoder(resp.Body).Decode(&llmResp); err != nil {
		log.Printf("Failed to decode response: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid response from LLM endpoint"})
		return
	}

	if len(llmResp.Choices) == 0 || llmResp.Choices[0].Message.Content == "" {
		log.Println("Invalid response structure from LLM")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid response structure from LLM endpoint"})
		return
	}

	content := llmResp.Choices[0].Message.Content
	c.JSON(http.StatusOK, ChatResponse{Content: content})
}

func handleLoadTest(c *gin.Context) {
	var req LoadTestRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get user info from headers
	userInfo := map[string]string{
		"user_id":    c.GetHeader("X-Forwarded-User"),
		"username":   c.GetHeader("X-Forwarded-Preferred-Username"),
		"email":      c.GetHeader("X-Forwarded-Email"),
		"request_id": c.GetHeader("X-Request-Id"),
		"client_ip":  c.GetHeader("X-Real-Ip"),
	}
	log.Printf("Load test initiated by user: %v", userInfo)

	// Create a new load test target
	target := fmt.Sprintf("http://localhost:%s/api", os.Getenv("DATABRICKS_APP_PORT"))
	rate := vegeta.Rate{Freq: req.SpawnRate, Per: time.Second}
	duration := time.Duration(req.TestTime) * time.Second

	// Create the attacker
	attacker := vegeta.NewAttacker()

	// Create a metrics collector
	metrics := &vegeta.Metrics{}

	// Create the target function with GET request instead of POST
	targeter := vegeta.NewStaticTargeter(vegeta.Target{
		Method: "GET",
		URL:    target,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	})

	// Add a counter to track requests
	for res := range attacker.Attack(targeter, rate, duration, "Load Test") {
		metrics.Add(res)
	}
	metrics.Close()
	// Prepare the response
	response := LoadTestResponse{
		TestDuration:       req.TestTime,
		TotalRequests:      int64(metrics.Requests),
		SuccessfulRequests: int64(metrics.Requests) * int64(metrics.Success),
		FailedRequests:     int64(metrics.Requests) * int64(1-metrics.Success),
		RequestsPerSecond:  metrics.Rate,
		ConcurrentUsers:    req.Users,
	}
	response.ResponseTime.Min = metrics.Latencies.Min
	response.ResponseTime.Max = metrics.Latencies.Max
	response.ResponseTime.Mean = metrics.Latencies.Mean
	response.ResponseTime.P95 = metrics.Latencies.P95
	response.ResponseTime.P99 = metrics.Latencies.P99

	for status, count := range metrics.StatusCodes {
		statusCode, _ := strconv.Atoi(status)
		if statusCode >= 400 {
			response.Errors = append(response.Errors, ErrorDetail{
				Name:      fmt.Sprintf("HTTP %s", status),
				Count:     int64(count),
				ErrorType: "HTTP Error",
			})
		}
	}

	// Format and log the metrics in a readable way
	log.Printf(`
Load Test Results:
-----------------
Duration: %d seconds
Total Requests: %d
Successful Requests: %d
Failed Requests: %d
Requests/second: %.2f
Concurrent Users: %d

Response Times:
--------------
Min: %s
Max: %s
Mean: %s
P95: %s
P99: %s

Errors: %v
`,
		response.TestDuration,
		response.TotalRequests,
		response.SuccessfulRequests,
		response.FailedRequests,
		response.RequestsPerSecond,
		response.ConcurrentUsers,
		response.ResponseTime.Min,
		response.ResponseTime.Max,
		response.ResponseTime.Mean,
		response.ResponseTime.P95,
		response.ResponseTime.P99,
		response.Errors,
	)

	c.JSON(http.StatusOK, response)
}

// Helper function to check if file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

func main() {
	StartGoServer()
}
