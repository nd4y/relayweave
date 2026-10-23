import os
import time
import acceptance as p
deadline=time.monotonic()+120
while time.monotonic()<deadline:
    try:
        status=p.json_url('http://127.0.0.1:39090/status')
        if len([row for row in status['cores'] if row['ready']])>=3 and p.proxy_http(38080)[0]==200:
            print('Three actual engines ready',flush=True);break
    except Exception:pass
    time.sleep(1)
else:raise AssertionError('worker did not restore three ready engines')
