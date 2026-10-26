"""Verify provisioned dashboards, their actual queries, alert rules and Loki."""
import json
import time
import urllib.parse
import urllib.request
from pathlib import Path
import full_acceptance as f

def main():
    f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
    origin=f.BASE.split('/api/')[0]
    def get(path):
        with f.client.open(origin+path,timeout=30) as r:return json.load(r)
    def prometheus(path):return get('/grafana/api/datasources/proxy/uid/relayweave-prometheus'+path)
    rows=get('/grafana/api/search?query=RelayWeave')
    dashboards=[x for x in rows if x.get('uid','').startswith('rw-')]
    assert len(dashboards)==6,(len(dashboards),rows)
    results=[]
    for row in dashboards:
        dashboard=get('/grafana/api/dashboards/uid/'+row['uid'])['dashboard']
        for panel in dashboard['panels']:
            if panel['type']=='logs':continue
            for target in panel.get('targets',[]):
                value=prometheus('/api/v1/query?'+urllib.parse.urlencode({'query':target['expr']}))
                assert value['status']=='success',(panel['title'],value)
                results.append({'dashboard':row['uid'],'panel':panel['title'],'query_valid':True,'series':len(value['data']['result'])})
    up=prometheus('/api/v1/query?'+urllib.parse.urlencode({'query':'up'}))
    assert all(float(x['value'][1])==1 for x in up['data']['result']),up
    expected_workers={x['id'] for x in f.request('workers')['items']}
    monitored_workers={x['metric'].get('worker') for x in up['data']['result'] if x['metric'].get('job')=='workers'}
    assert expected_workers==monitored_workers,(expected_workers,monitored_workers)
    rules=prometheus('/api/v1/rules')['data']['groups'];names=[rule['name'] for group in rules for rule in group['rules']]
    assert len(names)>=12,names
    loki=get('/grafana/api/datasources/proxy/uid/relayweave-loki/loki/api/v1/query_range?'+urllib.parse.urlencode({'query':'{app="relayweave"}','limit':5,'start':str(time.time_ns()-3600*10**9)}))
    assert loki['status']=='success' and len(loki['data']['result'])>0,loki
    report={'passed':True,'dashboards':6,'queries':results,'alert_rules':names,'loki_streams':len(loki['data']['result']),'admin_session_reused':True,'workers':sorted(monitored_workers)}
    Path('/reports/monitoring.json').write_text(json.dumps(report,indent=2));print(json.dumps({'passed':True,'dashboards':6,'queries':len(results),'rules':len(names),'loki_streams':len(loki['data']['result'])}),flush=True)
if __name__=='__main__':main()
