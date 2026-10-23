import full_acceptance as f
f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
for source in f.request('sources')['items']:
 f.patch(source,{'max_stale_seconds':604800})
f.operation('config/apply')
print('Synthetic sources valid for seven days; no external providers used.',flush=True)
