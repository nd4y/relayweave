"""Characterize native Xray UDP limits independently of the gateway/NAT."""
import json
import os
import socket
from acceptance import address, datagram_payload, socks_connect

for port in [19001, 19002]:
    control, bound = socks_connect(port, '0.0.0.0', 0, command=3)
    with control, socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as udp:
        udp.settimeout(.4)
        rows=[]
        for n in [0, 1, 4096, 5000, 6000, 7000, 7500, 7800, 7900, 8000, 4096]:
            value=os.urandom(n)
            udp.sendto(b'\0\0\0'+address('origin',9002)+value,bound)
            try:
                received=datagram_payload(udp.recvfrom(65535)[0])
                row={'payload_bytes':n,'returned_bytes':len(received),'passed':received==value}
            except socket.timeout:
                row={'payload_bytes':n,'passed':False,'reason':'timeout'}
            rows.append(row)
        print(json.dumps({'native_core_port':port,'measurements':rows}),flush=True)
