"""Pure integration contract tests; no provider or media inputs."""
import importlib.util
import os
from pathlib import Path
import sys
import types
import unittest

class RoutingTest(unittest.TestCase):
    def setUp(self):
        os.environ['RELAYWEAVE_DOWNLOAD_PATHS']='[{"port":8082,"policy_id":"a"},{"port":8083,"policy_id":"b"},{"port":8084,"policy_id":"c"}]'
        dl=types.ModuleType('dl_formats');dl.get_opts=lambda data:dict(data)
        yd=types.ModuleType('yt_dlp')
        class YDL:
            def __init__(self,params=None):self.params=params
        yd.YoutubeDL=YDL
        sys.modules['dl_formats']=dl;sys.modules['yt_dlp']=yd
        spec=importlib.util.spec_from_file_location('routing',Path(__file__).with_name('sitecustomize.py'))
        self.mod=importlib.util.module_from_spec(spec);spec.loader.exec_module(self.mod)
        self.status={'drained':False,'policies':[{'id':k,'ready_paths':[k]} for k in 'abc'],
            'routes':[{'profile_id':k,'server_ips':[ip],'active':load} for k,ip,load in
                      [('a','192.0.2.1',4),('b','192.0.2.1',0),('c','192.0.2.2',1)]]}
        self.mod._snapshot=lambda:self.status
        self.params={'proxy':'http://test:synthetic@relayweave-proxy:8082'}

    def test_shared_ip_load_and_stable_choice(self):
        result=self.mod._route(self.params)
        self.assertTrue(result['proxy'].endswith(':8084'))
        self.assertNotIn('_relayweave_path_selected',self.params)
        self.status['routes'][2]['active']=100
        self.assertEqual(result,self.mod._route(result))

    def test_unhealthy_path_and_auth(self):
        self.status['policies'][2]['ready_paths']=[]
        self.assertIn(self.mod._route(self.params)['proxy'].rsplit(':',1)[1],['8082','8083'])
        for p in self.status['policies']:p['ready_paths']=[]
        with self.assertRaisesRegex(RuntimeError,'No healthy'):self.mod._route(self.params)
        with self.assertRaisesRegex(RuntimeError,'authentication'):
            self.mod._route({'proxy':'http://relayweave-proxy:8082'})

    def test_hooks_and_explicit_other_proxy(self):
        self.assertTrue(self.mod.HOOK_ACTIVE)
        ydl=sys.modules['yt_dlp'].YoutubeDL(self.params)
        self.assertTrue(ydl.params['proxy'].endswith(':8084'))
        opts=sys.modules['dl_formats'].get_opts(self.params)
        self.assertTrue(opts['proxy'].endswith(':8084'))
        custom={'proxy':'http://custom.example:8080'}
        self.assertEqual(custom,self.mod._route(custom))

if __name__=='__main__':unittest.main()
