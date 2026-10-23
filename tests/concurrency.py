"""Real published SOCKS load through Xray/TLS to the isolated origin."""
import asyncio
import json
import os
import socket
import time
from pathlib import Path

TCP_COUNT=int(os.environ.get('TCP_COUNT','5000'))
UDP_COUNT=int(os.environ.get('UDP_COUNT','256'))
PORT=int(os.environ.get('SOCKS_PORT','41080'))

async def address(reader):
    kind=(await reader.readexactly(1))[0]
    if kind==1:host=socket.inet_ntoa(await reader.readexactly(4))
    elif kind==4:host=socket.inet_ntop(socket.AF_INET6,await reader.readexactly(16))
    elif kind==3:host=(await reader.readexactly((await reader.readexactly(1))[0])).decode()
    else:raise AssertionError('address type')
    return host,int.from_bytes(await reader.readexactly(2),'big')

async def connect(command,port):
    reader,writer=await asyncio.open_connection('127.0.0.1',PORT)
    try:
        writer.write(b'\x05\x01\x00');await writer.drain()
        assert await reader.readexactly(2)==b'\x05\x00'
        target=b'\x01\x00\x00\x00\x00' if command==3 else b'\x03\x06origin'
        writer.write(bytes([5,command,0])+target+port.to_bytes(2,'big'));await writer.drain()
        head=await reader.readexactly(3);assert head==b'\x05\x00\x00',head
        bound=await address(reader)
        return reader,writer,bound
    except BaseException:
        writer.close();await writer.wait_closed();raise

async def main():
    started=time.monotonic();connections=[];datagrams=[];sem=asyncio.Semaphore(64);retries=0
    loop=asyncio.get_running_loop()
    async def exchange(udp,frame,bound):
        nonlocal retries
        for attempt in range(5):
            await loop.sock_sendto(udp,frame,bound)
            try:
                reply,_=await asyncio.wait_for(loop.sock_recvfrom(udp,8192),1)
                assert reply.endswith(frame[13:]);return
            except TimeoutError:
                retries+=1
        raise AssertionError(f'UDP echo timeout after 5 transmissions, relay port {bound[1]}')
    async def open_tcp(i):
        async with sem:
            r,w,_=await asyncio.wait_for(connect(1,9001),20)
            payload=f'connection-{i}'.encode()
            w.write(payload);await w.drain()
            assert await asyncio.wait_for(r.readexactly(len(payload)),20)==payload
            connections.append((r,w,payload))
    try:
        await asyncio.gather(*(open_tcp(i) for i in range(TCP_COUNT)))
        print(f'Opened {len(connections)} verified TCP streams',flush=True)
        for i in range(UDP_COUNT):
            r,w,bound=await asyncio.wait_for(connect(3,0),15)
            connections.append((r,w,None))
            udp=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);udp.setblocking(False)
            frame=b'\0\0\0\x03\x06origin'+(9002).to_bytes(2,'big')+f'udp-{i}'.encode()
            try:await exchange(udp,frame,bound)
            except BaseException:
                udp.close();print(f'Failed UDP association {i+1}, verified {len(datagrams)}, retries {retries}',flush=True);raise
            datagrams.append((udp,bound,frame))
            if (i+1)%32==0:print(f'UDP verified {i+1}/{UDP_COUNT}',flush=True)
        print(f'Opened {len(datagrams)} verified UDP associations',flush=True)
        # Hold the full load and prove traffic remains usable on every stream.
        for round in range(3):
            await asyncio.sleep(10)
            async def verify(item):
                r,w,payload=item
                if payload:
                    w.write(payload);await w.drain()
                    assert await asyncio.wait_for(r.readexactly(len(payload)),20)==payload
            await asyncio.gather(*(verify(x) for x in connections))
            for udp,bound,frame in datagrams:
                await exchange(udp,frame,bound)
            print(f'Full load verification {round+1}/3 passed',flush=True)
        report={'passed':True,'tcp':TCP_COUNT,'udp':UDP_COUNT,'hold_seconds':30,'udp_retransmissions':retries,'elapsed_seconds':round_value(time.monotonic()-started)}
        Path('/reports/concurrency.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)
    finally:
        for udp,_,_ in datagrams:udp.close()
        for _,w,_ in connections:w.close()
        await asyncio.gather(*(w.wait_closed() for _,w,_ in connections),return_exceptions=True)

def round_value(x):return round(x,3)
asyncio.run(main())
