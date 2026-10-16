import json,sqlite3,time,urllib.request,urllib.error
import browser_review_t02 as b
RUN='shadow:fixture-failure:1:2';TASK='analysis:'+RUN

def read_state(label):
 data={'recorded_at_ms':int(time.time()*1000),'task':b.backend('history','/tasks/'+TASK)[1],'run':b.backend('history','/analysis-runs/'+RUN)[1],'steps':b.backend('history','/analysis-runs/'+RUN+'/steps')[1]}
 con=sqlite3.connect('file:'+str(b.HISTORY/'history.db')+'?mode=ro',uri=True);con.row_factory=sqlite3.Row
 data['river']=dict(con.execute('SELECT id,state,attempt,scheduled_at,json(metadata) AS metadata FROM river_job WHERE id=?',(data['task']['river_id'],)).fetchone());data['river']['metadata']=json.loads(data['river']['metadata'])
 data['audit']=[json.loads(row[0]) for row in con.execute('SELECT json(data) FROM audit WHERE request_id=? ORDER BY sequence',(TASK,))];con.close()
 b.R.json_write(b.OUT/(label+'.json'),data)
 return data

def action(action,version):
 credentials=b.access('history');url='http://127.0.0.1:19063/api/sf/v1/tasks/'+TASK+'/'+action
 req=urllib.request.Request(url,method='POST',headers={'Authorization':'REDACTED'+credentials['token'],'Content-Type':'application/json'},data=json.dumps({'expected_version':version}).encode())
 try:
  with urllib.request.urlopen(req,timeout=10) as response:status,raw=response.status,response.read()
 except urllib.error.HTTPError as e:status,raw=e.code,e.read()
 return {'action':action,'expected_version':version,'status':status,'response':json.loads(raw),'recorded_at_ms':int(time.time()*1000)}
