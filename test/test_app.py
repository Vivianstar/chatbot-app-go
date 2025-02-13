import unittest
import requests
import json
import time

class TestChatServer(unittest.TestCase):
    BASE_URL = "http://localhost:8000"

    def setUp(self):
        # Ensure server is running before tests
        try:
            requests.get(f"{self.BASE_URL}/api")
        except requests.exceptions.ConnectionError:
            self.fail("Server is not running. Please start the server before running tests.")

    def test_health_check(self):
        response = requests.get(f"{self.BASE_URL}/api")
        self.assertEqual(response.status_code, 200)
        data = response.json()
        self.assertIn("message", data)

    def test_chat_endpoint(self):
        payload = {
            "message": "Hello, how are you?"
        }
        response = requests.post(f"{self.BASE_URL}/api/chat", json=payload)
        self.assertEqual(response.status_code, 200)
        data = response.json()
        self.assertIn("content", data)
        self.assertIsInstance(data["content"], str)

    def test_load_test_endpoint(self):
        params = {
            "users": 10,
            "spawn_rate": 2,
            "test_time": 5
        }
        response = requests.get(f"{self.BASE_URL}/api/load-test", params=params)
        self.assertEqual(response.status_code, 200)
        data = response.json()
        
        # Verify response structure
        self.assertIn("test_duration", data)
        self.assertIn("total_requests", data)
        self.assertIn("successful_requests", data)
        self.assertIn("failed_requests", data)
        self.assertIn("requests_per_second", data)
        self.assertIn("concurrent_users", data)
        self.assertIn("response_time", data)

    def test_invalid_chat_request(self):
        payload = {
            "invalid_field": "This should fail"
        }
        response = requests.post(f"{self.BASE_URL}/api/chat", json=payload)
        self.assertEqual(response.status_code, 400)

    def test_load_test_invalid_params(self):
        params = {
            "users": -1,  # Invalid value
            "spawn_rate": 2,
            "test_time": 5
        }
        response = requests.get(f"{self.BASE_URL}/api/load-test", params=params)
        self.assertEqual(response.status_code, 400)

if __name__ == '__main__':
    unittest.main()
