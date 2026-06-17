import http.server
import socketserver
import json
import subprocess
import os

PORT = 8080
PUBLIC_DIR = os.path.join(os.path.dirname(__file__), 'public')

class DashboardHandler(http.server.SimpleHTTPRequestHandler):
    def translate_path(self, path):
        # Serve static files from PUBLIC_DIR
        original_path = super().translate_path(path)
        rel_path = os.path.relpath(original_path, os.getcwd())
        return os.path.join(PUBLIC_DIR, rel_path)

    def do_GET(self):
        if self.path == '/api/stats':
            self.handle_api_stats()
        else:
            # Serve index.html for root path
            if self.path == '/' or self.path == '':
                self.path = '/index.html'
            super().do_GET()

    def do_POST(self):
        if self.path == '/api/vibrate':
            self.handle_api_vibrate()
        elif self.path == '/api/tts':
            self.handle_api_tts()
        elif self.path == '/api/toast':
            self.handle_api_toast()
        else:
            self.send_error(404, "Not Found")

    def handle_api_stats(self):
        stats = {}
        
        # Get battery info
        try:
            battery_data = subprocess.check_output(['termux-battery-status'], timeout=2).decode('utf-8')
            stats['battery'] = json.loads(battery_data)
        except Exception as e:
            stats['battery'] = {"status": "Unavailable", "percentage": 0, "temperature": 0.0, "health": "UNKNOWN", "plugged": "UNPLUGGED"}

        # Get RAM info
        try:
            mem_data = subprocess.check_output(['free', '-m']).decode('utf-8')
            # Parse free -m output
            lines = mem_data.strip().split('\n')
            if len(lines) > 1:
                parts = lines[1].split()
                stats['ram'] = {
                    'total': int(parts[1]),
                    'used': int(parts[2]),
                    'free': int(parts[3]),
                    'shared': int(parts[4]) if len(parts) > 4 else 0,
                    'buff_cache': int(parts[5]) if len(parts) > 5 else 0,
                    'available': int(parts[6]) if len(parts) > 6 else int(parts[3])
                }
        except Exception as e:
            stats['ram'] = {'total': 0, 'used': 0, 'free': 0}

        # Get Storage info
        try:
            storage_data = subprocess.check_output(['df', '-h', '/data']).decode('utf-8')
            lines = storage_data.strip().split('\n')
            if len(lines) > 1:
                parts = lines[1].split()
                stats['storage'] = {
                    'size': parts[1],
                    'used': parts[2],
                    'avail': parts[3],
                    'percent': parts[4]
                }
        except Exception as e:
            stats['storage'] = {'size': '0', 'used': '0', 'avail': '0', 'percent': '0%'}

        # Get Uptime & Load Avg
        try:
            uptime_data = subprocess.check_output(['uptime']).decode('utf-8').strip()
            stats['uptime'] = uptime_data
        except Exception:
            stats['uptime'] = 'Unavailable'

        self.send_response(200)
        self.send_header('Content-type', 'application/json')
        self.send_header('Access-Control-Allow-Origin', '*')
        self.end_headers()
        self.wfile.write(json.dumps(stats).encode('utf-8'))

    def handle_api_vibrate(self):
        try:
            subprocess.run(['termux-vibrate', '-d', '500'], timeout=2)
            self.send_success({"status": "Vibrated"})
        except Exception as e:
            self.send_error_msg(str(e))

    def handle_api_tts(self):
        content_length = int(self.headers['Content-Length'])
        post_data = self.rfile.read(content_length)
        try:
            data = json.loads(post_data.decode('utf-8'))
            text = data.get('text', 'Hello from Termux Dashboard')
            subprocess.run(['termux-tts-speak', text], timeout=5)
            self.send_success({"status": "Spoken", "text": text})
        except Exception as e:
            self.send_error_msg(str(e))

    def handle_api_toast(self):
        content_length = int(self.headers['Content-Length'])
        post_data = self.rfile.read(content_length)
        try:
            data = json.loads(post_data.decode('utf-8'))
            text = data.get('text', 'Termux Alert!')
            subprocess.run(['termux-toast', text], timeout=2)
            self.send_success({"status": "Toasted", "text": text})
        except Exception as e:
            self.send_error_msg(str(e))

    def send_success(self, data):
        self.send_response(200)
        self.send_header('Content-type', 'application/json')
        self.send_header('Access-Control-Allow-Origin', '*')
        self.end_headers()
        self.wfile.write(json.dumps(data).encode('utf-8'))

    def send_error_msg(self, msg):
        self.send_response(500)
        self.send_header('Content-type', 'application/json')
        self.send_header('Access-Control-Allow-Origin', '*')
        self.end_headers()
        self.wfile.write(json.dumps({"error": msg}).encode('utf-8'))

# Allow address reuse
socketserver.TCPServer.allow_reuse_address = True

if __name__ == '__main__':
    # Ensure public dir exists
    if not os.path.exists(PUBLIC_DIR):
        os.makedirs(PUBLIC_DIR)
        
    print(f"Starting server at http://localhost:{PORT}")
    with socketserver.TCPServer(("", PORT), DashboardHandler) as httpd:
        try:
            httpd.serve_forever()
        except KeyboardInterrupt:
            print("\nShutting down server.")
