"""Isolated test upstream with live password rotation; no provider data."""
import base64
import hmac
import http.server
import json
import socket
import threading

lock=threading.Lock();epoch=1
def password():
 with lock:return 'fixture-old-password' if epoch==1 else 'fixture-new-password'

class Quiet(http.server.BaseHTTPRequestHandler):
 rbufsize=0
 protocol_version='HTTP/1.1'
 def log_message(self,*args):pass
 def send(self,status,body=b''):
  self.send_response(status)
  if status!=304:self.send_header('Content-Length',str(len(body)))
  self.send_header('Connection','close');self.end_headers();self.wfile.write(body);self.close_connection=True

class Control(Quiet):
 def do_POST(self):
  global epoch
  if self.path not in ('/rotate','/reset'):self.send(404);return
  with lock:epoch=2 if self.path=='/rotate' else 1
  self.send(200,b'{}')
 def do_GET(self):
  if self.path!='/subscription':self.send(404);return
  with lock:current=epoch
  etag='"credentials-%d"'%current
  if self.headers.get('If-None-Match')==etag:self.send(304);return
  body=json.dumps({'outbounds':[{'tag':'DE Rotating credential fixture','protocol':'http','settings':{'servers':[{'address':'credential-upstream','port':9009,'users':[{'user':'lab-user','pass':'fixture-old-password' if current==1 else 'fixture-new-password'}]}]}}]}).encode()
  self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('ETag',etag);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)

class Proxy(Quiet):
 def do_CONNECT(self):
  expected='Basic '+base64.b64encode(('lab-user:'+password()).encode()).decode()
  if not hmac.compare_digest(self.headers.get('Proxy-Authorization',''),expected):self.send(407);return
  if self.path not in ('origin:9000','origin:9001','origin:9443'):self.send(403);return
  host,port=self.path.rsplit(':',1)
  try:upstream=socket.create_connection((host,int(port)),timeout=8)
  except OSError:self.send(502);return
  self.send_response(200);self.end_headers();self.connection.settimeout(None);upstream.settimeout(None)
  def copy(source,target):
   try:
    while True:
     data=source.recv(65536)
     if not data:break
     target.sendall(data)
   except OSError:pass
   finally:
    try:target.shutdown(socket.SHUT_WR)
    except OSError:pass
  thread=threading.Thread(target=copy,args=(self.connection,upstream),daemon=True);thread.start()
  try:copy(upstream,self.connection)
  finally:upstream.close();self.connection.close();thread.join(timeout=2)

class Server(http.server.ThreadingHTTPServer):
 daemon_threads=True
 allow_reuse_address=True

if __name__=='__main__':
 proxy=Server(('0.0.0.0',9009),Proxy)
 threading.Thread(target=proxy.serve_forever,daemon=True).start()
 Server(('0.0.0.0',9008),Control).serve_forever()
