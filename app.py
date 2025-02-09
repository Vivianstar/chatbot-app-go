import logging
from fastapi import FastAPI, HTTPException, Request
import os
import stat
from fastapi.middleware.cors import CORSMiddleware
from fastapi.staticfiles import StaticFiles
import subprocess
import uvicorn
from contextlib import asynccontextmanager
# Add logging configuration at the top
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(levelname)s - %(message)s'
)

def start_go_proxy():
    current_dir = os.path.dirname(os.path.abspath(__file__))
    go_executable = os.path.join(current_dir, "main")
    os.chmod(go_executable, stat.S_IRWXU | stat.S_IRGRP | stat.S_IXGRP | stat.S_IROTH | stat.S_IXOTH)
    
    try:
        # Run the Go proxy as a background process
        process = subprocess.run([go_executable],
                         capture_output=True,   
                         text=True)
        logging.info("Go proxy server started successfully")
        return process
    except Exception as e:
        logging.error(f"Failed to start Go proxy: {str(e)}")
        return None


if __name__ == "__main__":
    start_go_proxy() 