"""Synthetic IPv6-only HTTP/TCP/UDP and UDP/TCP DNS inside its own bridge."""
import ipaddress,json,socket,socketserver,struct,threading,time
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer

class IPv6Server(socketserver.ThreadingTCPServer):
 address_family=socket.AF_INET6;allow_reuse_address=True;daemon_threads=True
class IPv6UDP(socketserver.ThreadingUDPServer):
 address_family=socket.AF_INET6;allow_reuse_address=True;daemon_threads=True
class HTTP6(ThreadingHTTPServer):address_family=socket.AF_INET6
class HTTP(BaseHTTPRequestHandler):
 def do_GET(self):self.send_response(200);self.send_header('Content-Length','3');self.end_headers();self.wfile.write(b'ok\n')
 def log_message(self,*args):pass
class Echo(socketserver.BaseRequestHandler):
 def handle(self):
  while data:=self.request.recv(65536):self.request.sendall(data)
class EchoUDP(socketserver.BaseRequestHandler):
 def handle(self):self.request[1].sendto(self.request[0],self.client_address)
def answer(data):
 qtype=struct.unpack('!H',data[-4:-2])[0];raw=ipaddress.ip_address('::1' if qtype==28 else '127.0.0.1').packed
 return data[:2]+b'\x81\x80\x00\x01\x00\x01\x00\x00\x00\x00'+data[12:]+b'\xc0\x0c'+struct.pack('!HHIH',qtype,1,60,len(raw))+raw
class DNSUDP(socketserver.BaseRequestHandler):
 def handle(self):self.request[1].sendto(answer(self.request[0]),self.client_address)
class DNSTCP(socketserver.BaseRequestHandler):
 def handle(self):
  prefix=self.request.recv(2)
  if len(prefix)!=2:return
  size=int.from_bytes(prefix,'big');data=b''
  while len(data)<size:
   part=self.request.recv(size-len(data))
   if not part:return
   data+=part
  reply=answer(data);self.request.sendall(len(reply).to_bytes(2,'big')+reply)
for cls,port,handler in [(HTTP6,9000,HTTP),(IPv6Server,9001,Echo),(IPv6UDP,9002,EchoUDP),(IPv6UDP,9003,DNSUDP),(IPv6Server,9003,DNSTCP)]:
 server=cls(('::',port),handler);threading.Thread(target=server.serve_forever,daemon=True).start()
print('Synthetic IPv6 fixture ready',flush=True)
while True:time.sleep(60)
