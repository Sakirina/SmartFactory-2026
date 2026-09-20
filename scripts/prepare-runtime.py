#!/usr/bin/env python3
"""Prepare a new customer deployment using an existing Linux release and pinned images."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle',type=Path,required=True)
    parser.add_argument('--directory',type=Path,required=True,help='new private state directory')
    parser.add_argument('--profile',choices=['capacity','site'],required=True)
    parser.add_argument('--hosting',choices=['hosted','self'],required=True)
    parser.add_argument('--simulation',choices=['fleet','scenes'],default='fleet')
    parser.add_argument('--public-host',default='localhost')
    parser.add_argument('--pki-binary',type=Path,help='override only the preparation host PKI utility')
    args=parser.parse_args()
    if args.profile=='site' and args.simulation=='scenes':parser.error('site uses fleet devices with independent node identifiers; use capacity for the five-scene demonstration')
    bundle=args.bundle.resolve();work=args.directory.resolve()
    if not (bundle/'bin/sf-edge').is_file():raise SystemExit('Linux release binaries are missing')
    os.umask(0o077);work.mkdir(parents=True,exist_ok=False)
    pki=work/'pki';pki.mkdir()
    pki_binary=str(args.pki_binary.resolve() if args.pki_binary else bundle/'bin/sf-pki')
    def private(name,value):
        path=work/name;path.parent.mkdir(parents=True,exist_ok=True)
        path.write_text(value if isinstance(value,str) else json.dumps(value,ensure_ascii=False,indent=2)+'\n')
        path.chmod(0o600);return path
    def certificate(node,hosts='',client=False):
        command=[pki_binary,'--dir',str(pki),'--node',node]
        if hosts:command+=['--hosts',hosts]
        if client:command+=['--client']
        subprocess.run(command,check=True,stdout=subprocess.DEVNULL)
    subprocess.run([pki_binary,'--mode','init','--dir',str(pki)],check=True,stdout=subprocess.DEVNULL)
    project='smartfactory_'+hashlib.sha256(str(work).encode()).hexdigest()[:10]
    nodes=['edge-a'] if args.profile=='capacity' else ['edge-a','edge-b','edge-c']
    credentials={key:secrets.token_urlsafe(32) for key in ['password','service_token','postgres_password','tb_admin_password','tb_sysadmin_password','nats_token','route_password']}
    private('deployment-credentials.json',credentials)
    private('development-credentials.json',{'password':credentials['password'],'service_token':credentials['service_token']})
    certificate('cloud-1','cloud,localhost,127.0.0.1,'+args.public_host)
    certificate('web','localhost,127.0.0.1,'+args.public_host)
    for node in ['cloud-1','config',*nodes]:
        private(node+'.master',base64.b64encode(os.urandom(32)).decode())
        if node.startswith('edge-'):certificate(node,node+',localhost,127.0.0.1',True)
        raw=subprocess.check_output([pki_binary,'--mode','public-keys','--dir',str(pki),'--node',node,'--master-key',str(work/(node+'.master'))]) if node!='config' else b'{}'
        private('pki/'+node+'.public.json',json.loads(raw))
    images=json.loads((bundle/'deploy/images.lock.json').read_text())['images']
    logging={'driver':'json-file','options':{'max-size':'5m','max-file':'2'}}
    services={};volumes={}
    def service(name,image,**values):
        services[name]={'image':images[image]['pinned'],'platform':'linux/amd64','pull_policy':'never','restart':'unless-stopped','logging':logging,**values}
    def pg(name,database):
        volumes[name]={}
        sql='CREATE DATABASE smartfactory;\n'+('CREATE DATABASE sf_config;\n' if name=='cloud-db' else '')
        path=private(name+'.sql',sql);path.chmod(0o644)
        service(name,'postgres',environment={'POSTGRES_DB':database,'POSTGRES_PASSWORD':credentials['postgres_password']},volumes=[name+':/var/lib/postgresql/data',str(path)+':/docker-entrypoint-initdb.d/business.sql:ro'],
                command=['postgres','-c','shared_buffers=128MB'],mem_limit='512m',healthcheck={'test':['CMD-SHELL','pg_isready -h 127.0.0.1 -U postgres -d '+database],'interval':'5s','timeout':'3s','retries':60})
    pg('cloud-db','thingsboard')
    for node in nodes:pg(node+'-db','tb_edge')
    volumes['kafka']={}
    service('kafka-storage-init','runtime',restart='no',user='0:0',command=['chown','1000:1000','/storage'],volumes=['kafka:/storage'])
    kafka_env={'KAFKA_NODE_ID':'1','KAFKA_PROCESS_ROLES':'broker,controller','KAFKA_LISTENERS':'PLAINTEXT://:9092,CONTROLLER://:9093','KAFKA_ADVERTISED_LISTENERS':'PLAINTEXT://kafka:9092','KAFKA_LISTENER_SECURITY_PROTOCOL_MAP':'CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT','KAFKA_CONTROLLER_QUORUM_VOTERS':'1@kafka:9093','KAFKA_CONTROLLER_LISTENER_NAMES':'CONTROLLER','KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR':'1','KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR':'1','KAFKA_TRANSACTION_STATE_LOG_MIN_ISR':'1','KAFKA_LOG_RETENTION_HOURS':'48','KAFKA_LOG_DIRS':'/tmp/kraft-combined-logs','KAFKA_HEAP_OPTS':'-Xms256m -Xmx512m'}
    service('kafka','kafka',environment=kafka_env,volumes=['kafka:/tmp/kraft-combined-logs'],mem_limit='1g',depends_on={'kafka-storage-init':{'condition':'service_completed_successfully'}},healthcheck={'test':['CMD-SHELL','bash -ec "exec 3<>/dev/tcp/127.0.0.1/9092"'],'interval':'10s','timeout':'10s','retries':60})
    tb_tuning={'MALLOC_ARENA_MAX':'2','LOCAL_JS_THREAD_POOL_SIZE':'4','LOCAL_JS_SANDBOX_MONITOR_THREAD_POOL_SIZE':'1','TBEL_THREAD_POOL_SIZE':'4','ACTORS_RULE_EXTERNAL_CALL_THREAD_POOL_SIZE':'8'}
    tb_env={**tb_tuning,'SPRING_DATASOURCE_URL':'jdbc:postgresql://cloud-db:5432/thingsboard','SPRING_DATASOURCE_USERNAME':'postgres','SPRING_DATASOURCE_PASSWORD':credentials['postgres_password'],'TB_QUEUE_TYPE':'kafka','TB_KAFKA_SERVERS':'kafka:9092','TB_QUEUE_KAFKA_BOOTSTRAP_SERVERS':'kafka:9092','EDGES_ENABLED':'true','JS_EVALUATOR':'local','JAVA_OPTS':'-Xms384m -Xmx768m -XX:MaxMetaspaceSize=384m -XX:MaxDirectMemorySize=128m -XX:ReservedCodeCacheSize=128m -XX:ActiveProcessorCount=4 -Xss512k','TB_SERVICE_ID':'sf-cloud-native'}
    dependencies={'cloud-db':{'condition':'service_healthy'},'kafka':{'condition':'service_healthy'}}
    service('tb-install','thingsboard',restart='no',profiles=['install'],environment={**tb_env,'INSTALL_TB':'true','LOAD_DEMO':'false'},depends_on=dependencies)
    service('thingsboard','thingsboard',environment=tb_env,depends_on=dependencies,ports=['127.0.0.1:18080:8080'],mem_limit='4g')
    common={'SF_BOOTSTRAP_PASSWORD':credentials['password'],'SF_SERVICE_TOKEN':credentials['service_token'],'SF_STATIC_DIR':'/bundle/Frontends/dist'}
    def runtime(name,binary,environment,**values):
        service(name,'runtime',entrypoint=['/bundle/bin/'+binary],environment={**common,**environment},volumes=[str(bundle)+':/bundle:ro',str(work)+':/private:ro'],
                user=str(os.getuid())+':'+str(os.getgid()),read_only=True,tmpfs=['/tmp:rw,size=64m'],cap_drop=['ALL'],security_opt=['no-new-privileges:true'],**values)
    def dsn(host,database):return 'postgres://postgres:'+credentials['postgres_password']+'@'+host+':5432/'+database+'?sslmode=disable'
    runtime('config','sf-config',{'SF_NODE_ID':'config','SF_LISTEN':'0.0.0.0:8092','SF_DATABASE':dsn('cloud-db','sf_config'),'SF_MASTER_KEY_FILE':'/private/config.master'},depends_on={'cloud-db':{'condition':'service_healthy'}},mem_limit='256m')
    cloud_env={'SF_NODE_ID':'cloud-1','SF_LISTEN':'0.0.0.0:8090','SF_DATABASE':dsn('cloud-db','smartfactory'),'SF_MASTER_KEY_FILE':'/private/cloud-1.master','SF_CONFIG_URL':'http://config:8092','SF_TB_URL':'http://thingsboard:8080','SF_TB_USERNAME':'platform@smartfactory.local','SF_TB_PASSWORD':credentials['tb_admin_password'],'SF_TB_CALLBACK_URL':'http://cloud:8090','SF_SYNC_LISTEN':'0.0.0.0:18443','SF_TLS_CA':'/private/pki/ca.pem','SF_TLS_CERT':'/private/pki/cloud-1.pem','SF_TLS_KEY':'/private/pki/cloud-1.key','SF_NATIVE_RELAY_LISTEN':'0.0.0.0:18444','SF_NATIVE_RELAY_TARGET':'thingsboard:7070'}
    runtime('cloud','sf-cloud',cloud_env,command=['--seed'],ports=['127.0.0.1:8090:8090'],depends_on={'cloud-db':{'condition':'service_healthy'},'config':{'condition':'service_started'}},mem_limit='768m')
    signing=json.loads((pki/'cloud-1.public.json').read_text())['audit_public_key']
    if args.profile=='site':
        def conf(value,depth=0):
            return '\n'.join('  '*depth+k+(' {\n'+conf(v,depth+1)+'\n'+'  '*depth+'}' if isinstance(v,dict) else ': '+json.dumps(v)) for k,v in value.items())
        for i in range(1,4):
            name='nats-'+str(i);certificate(name,name+',localhost,127.0.0.1',True);volumes[name]={}
            tls={'cert_file':'/certs/'+name+'.pem','key_file':'/certs/'+name+'.key','ca_file':'/certs/ca.pem','verify':True,'timeout':3}
            value={'server_name':name,'listen':'0.0.0.0:4222','tls':tls,'authorization':{'token':credentials['nats_token']},'jetstream':{'store_dir':'/data','max_memory_store':67108864,'max_file_store':2147483648},'cluster':{'name':project,'listen':'0.0.0.0:6222','advertise':name+':6222','tls':tls,'authorization':{'user':'site','password':credentials['route_password']},'routes':[f"nats-route://site:{credentials['route_password']}@nats-{p}:6222" for p in range(1,4) if p!=i]}}
            private(name+'.conf',conf(value)+'\n')
            service(name,'nats',command=['-c','/config/nats.conf'],volumes=[str(work/(name+'.conf'))+':/config/nats.conf:ro',str(pki)+':/certs:ro',name+':/data'],mem_limit='256m',user=str(os.getuid())+':'+str(os.getgid()))
            # The image's default user initializes named storage with appropriate ownership.
            services[name].pop('user')
    portals=[('cloud',8090,8443)]
    for index,node in enumerate(nodes):
        port=8091 if index==0 else 8092+index
        network=node+'-network'
        service(network,'runtime',command=['sleep','infinity'],ports=[f'127.0.0.1:{port}:8091',f'127.0.0.1:{18081+index}:8080',f'127.0.0.1:{19083+index}:18083'],
                networks={'default':{'aliases':[node]}},read_only=True,cap_drop=['ALL'],security_opt=['no-new-privileges:true'],mem_limit='32m')
        env={'SF_NODE_ID':node,'SF_LISTEN':'0.0.0.0:8091','SF_DATABASE':dsn(node+'-db','smartfactory'),'SF_MASTER_KEY_FILE':'/private/'+node+'.master','SF_CONFIG_URL':'http://config:8092','SF_DATATRANSFER_ADDRESS':'127.0.0.1:50051','SF_SYNC_URL':'https://cloud:18443','SF_CLOUD_SIGNING_KEY':signing,'SF_TLS_CA':'/private/pki/ca.pem','SF_TLS_CERT':'/private/pki/'+node+'.pem','SF_TLS_KEY':'/private/pki/'+node+'.key','SF_NATIVE_RELAY_LISTEN':'127.0.0.1:17071','SF_NATIVE_RELAY_TARGET':'cloud:18444','SF_TB_URL':'http://127.0.0.1:8080','SF_TB_USERNAME':'platform@smartfactory.local','SF_TB_PASSWORD':credentials['tb_admin_password'],'SF_TB_CALLBACK_URL':'http://127.0.0.1:8091'}
        if args.profile=='site':env.update({'SF_NATS_URL':','.join('tls://nats-'+str(i)+':4222' for i in range(1,4)),'SF_NATS_TOKEN':credentials['nats_token'],'SF_NATS_PREFIX':project,'SF_NATS_CA':'/private/pki/ca.pem','SF_NATS_CERT':'/private/pki/'+node+'.pem','SF_NATS_KEY':'/private/pki/'+node+'.key'})
        runtime(node,'sf-edge',env,network_mode='service:'+network,depends_on={network:{'condition':'service_started'},node+'-db':{'condition':'service_healthy'},'cloud':{'condition':'service_started'}},mem_limit='512m')
        if args.simulation=='scenes':services[node]['command']=['--seed']
        folder=work/node;folder.mkdir()
        sim=['--state','/state/simulator.db','--config-out','/state/datatransfer.yaml','--http','0.0.0.0:18083']
        if args.simulation=='fleet':sim+=['--fleet-count','500' if args.profile=='capacity' else '50','--fleet-prefix',node,'--interval','3s' if args.profile=='capacity' else '5s']
        for suffix,binary,command in [('simulator','dt-simulator',sim),('gateway','datatransfer',['--config','/state/datatransfer.yaml'])]:
            runtime(node+'-'+suffix,binary,{},command=command,network_mode='service:'+network,depends_on={node:{'condition':'service_started'}},mem_limit='256m')
            services[node+'-'+suffix]['volumes'].append(str(folder)+':/state')
        services[node+'-simulator']['healthcheck']={'test':['CMD','wget','-q','-O','/dev/null','http://127.0.0.1:18083/state'],'interval':'2s','timeout':'2s','retries':30}
        services[node+'-gateway']['depends_on'][node+'-simulator']={'condition':'service_healthy'}
        private('tb-edge-'+node+'.env','# Written by bootstrap-runtime.py\n')
        native_env={**tb_tuning,'SPRING_DATASOURCE_URL':'jdbc:postgresql://'+node+'-db:5432/tb_edge','SPRING_DATASOURCE_USERNAME':'postgres','SPRING_DATASOURCE_PASSWORD':credentials['postgres_password'],'CLOUD_RPC_HOST':'127.0.0.1','CLOUD_RPC_PORT':'17071','CLOUD_RPC_SSL_ENABLED':'false','JAVA_OPTS':'-Xms256m -Xmx512m -XX:MaxMetaspaceSize=256m -XX:MaxDirectMemorySize=128m -XX:ReservedCodeCacheSize=128m -XX:ActiveProcessorCount=2 -Xss512k'}
        volumes[node+'-native-data']={}
        service(node+'-native','thingsboard_edge',profiles=['native-edge'],environment=native_env,env_file=[str(work/('tb-edge-'+node+'.env'))],network_mode='service:'+network,depends_on={node:{'condition':'service_started'}},volumes=[node+'-native-data:/data'],mem_limit='2g')
        portals.append((node,8091,8444+index))
    listeners=[];clusters=[]
    for name,port,public in portals:
        listeners.append({'name':name,'address':{'socket_address':{'address':'0.0.0.0','port_value':public}},'filter_chains':[{'transport_socket':{'name':'envoy.transport_sockets.tls','typed_config':{'@type':'type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext','common_tls_context':{'tls_certificates':[{'certificate_chain':{'filename':'/certs/web.pem'},'private_key':{'filename':'/certs/web.key'}}]}}},'filters':[{'name':'envoy.filters.network.http_connection_manager','typed_config':{'@type':'type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager','stat_prefix':name,'route_config':{'name':name,'virtual_hosts':[{'name':name,'domains':['*'],'routes':[{'match':{'prefix':'/'},'route':{'cluster':name,'timeout':'0s'}}]}]},'http_filters':[{'name':'envoy.filters.http.router','typed_config':{'@type':'type.googleapis.com/envoy.extensions.filters.http.router.v3.Router'}}]}}]}]})
        clusters.append({'name':name,'connect_timeout':'3s','type':'STRICT_DNS','load_assignment':{'cluster_name':name,'endpoints':[{'lb_endpoints':[{'endpoint':{'address':{'socket_address':{'address':name,'port_value':port}}}}]}]}})
    private('web.json',{'static_resources':{'listeners':listeners,'clusters':clusters}})
    service('web','envoy',command=['-c','/config/web.json','--log-level','warn'],volumes=[str(work/'web.json')+':/config/web.json:ro',str(pki)+':/certs:ro'],ports=[f'{public}:{public}' for _,_,public in portals],depends_on={name:{'condition':'service_started'} for name,_,_ in portals},mem_limit='256m')
    private('compose.json',{'name':project,'services':services,'volumes':volumes})
    private('profile.json',{'profile':args.profile,'hosting':args.hosting,'simulation':args.simulation,'nodes':nodes,'bundle':str(bundle),'project':project,'public_host':args.public_host,'device_count':500 if args.profile=='capacity' and args.simulation=='fleet' else 50*len(nodes) if args.simulation=='fleet' else 5*len(nodes),'native_cloud_url':'http://127.0.0.1:18080','business_cloud_url':'http://127.0.0.1:8090','urls':['https://'+args.public_host+':'+str(port) for _,_,port in portals]})
    subprocess.run(['docker','compose','-f',str(work/'compose.json'),'--profile','install','--profile','native-edge','config','--quiet'],check=True)
    print('Prepared and validated '+args.profile+' / '+args.hosting+' deployment at '+str(work))
    print('Trust the generated customer CA for browser access: '+str(pki/'ca.pem'))


if __name__=='__main__':main()
