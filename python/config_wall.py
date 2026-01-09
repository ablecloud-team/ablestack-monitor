'''
Copyright (c) 2021 ABLECLOUD Co. Ltd
이 파일은 Wall VM을 구성할 때 prometheus.yml, defaults.ini, db 파일 등을 설정하는 프로그램입니다.
최초 작성일 : 2021. 08. 25
'''

#!/usr/bin/python3
# -*- coding: utf-8 -*-

import yaml
import argparse
import json
import os
import sh
from ablestack import *
import configparser
import sqlite3
import re
import pymysql
from sh import cp
from sh import systemctl
from sh import ssh
from sh import python3

# prometheus에서 수집하는 exporter 및 서비스 포트
wall_prometheus_port = ":3001"
libvirt_exporter_port = ":3002"
node_exporter_port = ":3003"
process_exporter_port = ":3004"
blackbox_exporter_port = ":3005"
ipmi_exporter_port = ":3006"
cube_service_port = ":9090"
mold_service_port = ":8080"
mold_db_port = ":3306"
glue_prometheus_port = ":9095"

'''
함수명 : parseArgs
이 함수는 python library argparse를 시용하여 함수를 실행될 때 필요한 파라미터를 입력받고 파싱하는 역할을 수행합니다.
예를들어 action을 요청하면 해당 action일 때 요구되는 파라미터를 입력받고 해당 코드를 수행합니다.
'''

json_file_path = "/etc/cluster.json"
def parseArgs():
    parser = argparse.ArgumentParser(description='Prometheus Yaml file parsing and replace targets',
                                     epilog='copyrightⓒ 2021 All rights reserved by ABLECLOUD™')

    parser.add_argument('action', choices=[
                        'config', 'update', 'glueDsUpdate'], help='choose one of the actions')
    parser.add_argument('--cube', metavar='name', type=str,
                        nargs='*', help='cube ips')
    parser.add_argument('--scvm', metavar='name', type=str,
                        nargs='*', help='scvm ips')
    parser.add_argument('--ccvm', metavar='name', type=str,
                        nargs='*', help='ccvm ips')

    return parser.parse_args()

def openClusterJson():
    try:
        with open(json_file_path, 'r') as json_file:
            ret = json.load(json_file)
    except Exception as e:
        ret = createReturn(code=500, val='cluster.json read error')
        print ('EXCEPTION : ',e)

    return ret

json_data = openClusterJson()
os_type = json_data["clusterConfig"]["type"]

def cubeServiceConfig(cube_ip):
    cube = cube_ip.copy()
    for i in range(len(cube)):
        cube[i] = cube[i] + cube_service_port
    return cube


def moldServiceConfig(ccvm_ip):
    ccvm = ccvm_ip.copy()
    for i in range(len(ccvm)):
        ccvm[i] = ccvm[i] + mold_service_port
    return ccvm


def moldDBConfig(ccvm_ip):
    ccvm = ccvm_ip.copy()
    for i in range(len(ccvm)):
        ccvm[i] = ccvm[i] + mold_db_port
    return ccvm


def libvirtConfig(cube_ip):
    cube = cube_ip.copy()
    for i in range(len(cube)):
        cube[i] = cube[i] + libvirt_exporter_port
    return cube


def cubeNodeConfig(cube_ip):
    cube = cube_ip.copy()
    for i in range(len(cube)):
        cube[i] = cube[i] + node_exporter_port
    return cube

def ipmiConfig(cube_ip):
    cube = cube_ip.copy()
    for i in range(len(cube)):
        cube[i] = cube[i] + ipmi_exporter_port
    return cube

def scvmNodeConfig(scvm_ip):
    scvm = scvm_ip.copy()
    for i in range(len(scvm)):
        scvm[i] = scvm[i] + node_exporter_port
    return scvm


def ccvmNodeConfig(ccvm_ip):
    ccvm = ccvm_ip.copy()
    for i in range(len(ccvm)):
        ccvm[i] = ccvm[i] + node_exporter_port
    return ccvm


def wallPrometheusConfig(ccvm_ip):
    ccvm = ccvm_ip.copy()
    for i in range(len(ccvm)):
        ccvm[i] = ccvm[i] + wall_prometheus_port
    return ccvm


def gluePrometheusConfig(scvm_ip):
    scvm = scvm_ip.copy()
    for i in range(len(scvm)):
        scvm[i] = scvm[i] + glue_prometheus_port
    return scvm


def cubeProcessConfig(cube_ip):
    cube = cube_ip.copy()
    for i in range(len(cube)):
        cube[i] = cube[i] + process_exporter_port
    return cube


def scvmProcessConfig(scvm_ip):
    scvm = scvm_ip.copy()
    for i in range(len(scvm)):
        scvm[i] = scvm[i] + process_exporter_port
    return scvm


def ccvmProcessConfig(ccvm_ip):
    ccvm = ccvm_ip.copy()
    for i in range(len(ccvm)):
        ccvm[i] = ccvm[i] + process_exporter_port
    return ccvm


def cubeBlackboxConfig(cube_ip):
    cube = cube_ip.copy()
    for i in range(len(cube)):
        cube[i] = cube[i]
    return cube


def scvmBlackboxConfig(scvm_ip):
    scvm = scvm_ip.copy()
    for i in range(len(scvm)):
        scvm[i] = scvm[i]
    return scvm

def ccvmBlackboxConfig(ccvm_ip):
    ccvm = ccvm_ip.copy()
    for i in range(len(ccvm)):
        ccvm[i] = ccvm[i]
    return ccvm

def ccvmBlackboxConfigReplacement(ccvm_ip):
    ccvm = ccvm_ip.copy()
    for i in range(len(ccvm)):
        ccvm[i] = ccvm[i] + blackbox_exporter_port
    return ccvm

# 함수명 : configYaml
# 주요 기능 : 입력 받은 ip를 prometheus.yml 파일의 targets에 설정


def configYaml(cube, ccvm, scvm=None):
    prometheus_yml_path = '/usr/share/ablestack/ablestack-wall/prometheus/prometheus.yml'

    with open(prometheus_yml_path) as f:
        prometheus_org = yaml.safe_load(f)
    for i in range(len(prometheus_org['scrape_configs'])):

        if os_type == "ablestack-hci":
            if prometheus_org['scrape_configs'][i]['job_name'] == 'libvirt':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = libvirtConfig(
                    cube)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'cube':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = cubeNodeConfig(
                    cube)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'ipmi':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = ipmiConfig(
                    cube)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'scvm':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = scvmNodeConfig(
                    scvm)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'ccvm':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = ccvmNodeConfig(
                    ccvm)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'cube-process-exporter':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = cubeProcessConfig(
                    cube)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'scvm-process-exporter':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = scvmProcessConfig(
                    scvm)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'ccvm-process-exporter':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = ccvmProcessConfig(
                    ccvm)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'cube-blackbox':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = cubeBlackboxConfig(
                    cube)
                prometheus_org['scrape_configs'][i]['relabel_configs'][-1]['replacement'] = ccvmBlackboxConfigReplacement(
                    ccvm)[0]

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'scvm-blackbox':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = scvmBlackboxConfig(
                    scvm)
                prometheus_org['scrape_configs'][i]['relabel_configs'][-1]['replacement'] = ccvmBlackboxConfigReplacement(
                    ccvm)[0]

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'ccvm-blackbox':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = ccvmBlackboxConfig(
                    ccvm)
                prometheus_org['scrape_configs'][i]['relabel_configs'][-1]['replacement'] = ccvmBlackboxConfigReplacement(
                    ccvm)[0]

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'blackbox-tcp':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = cubeServiceConfig(cube) + moldServiceConfig(ccvm) + moldDBConfig(ccvm) + libvirtConfig(cube) + cubeNodeConfig(cube) + ipmiConfig(cube) + scvmNodeConfig(scvm) + ccvmNodeConfig(
                    ccvm) + cubeProcessConfig(cube) + scvmProcessConfig(scvm) + ccvmProcessConfig(ccvm) + ccvmBlackboxConfigReplacement(ccvm)
                prometheus_org['scrape_configs'][i]['relabel_configs'][-1]['replacement'] = ccvmBlackboxConfigReplacement(
                    ccvm)[0]
        else :
            if prometheus_org['scrape_configs'][i]['job_name'] == 'libvirt':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = libvirtConfig(
                    cube)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'cube':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = cubeNodeConfig(
                    cube)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'ipmi':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = ipmiConfig(
                    cube)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'ccvm':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = ccvmNodeConfig(
                    ccvm)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'cube-process-exporter':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = cubeProcessConfig(
                    cube)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'ccvm-process-exporter':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = ccvmProcessConfig(
                    ccvm)

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'cube-blackbox':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = cubeBlackboxConfig(
                    cube)
                prometheus_org['scrape_configs'][i]['relabel_configs'][-1]['replacement'] = ccvmBlackboxConfigReplacement(
                    ccvm)[0]

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'ccvm-blackbox':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = ccvmBlackboxConfig(
                    ccvm)
                prometheus_org['scrape_configs'][i]['relabel_configs'][-1]['replacement'] = ccvmBlackboxConfigReplacement(
                    ccvm)[0]

            elif prometheus_org['scrape_configs'][i]['job_name'] == 'blackbox-tcp':
                prometheus_org['scrape_configs'][i]['static_configs'][0]['targets'] = cubeServiceConfig(cube) + moldServiceConfig(ccvm) + moldDBConfig(ccvm) + libvirtConfig(cube) + cubeNodeConfig(cube) + ipmiConfig(cube) + ccvmNodeConfig(
                    ccvm) + cubeProcessConfig(cube)  + ccvmProcessConfig(ccvm) + ccvmBlackboxConfigReplacement(ccvm)
                prometheus_org['scrape_configs'][i]['relabel_configs'][-1]['replacement'] = ccvmBlackboxConfigReplacement(
                    ccvm)[0]

        with open(prometheus_yml_path, 'w') as yaml_file:
            yaml_file.write(
                yaml.dump(prometheus_org, default_flow_style=False))

# 함수명 : configIni
# 주요 기능 : grafana의 설정 파일에 도메인을 ccvm ip로 설정


def configIni(ccvm):
    ini_file = '/usr/share/ablestack/ablestack-wall/grafana/conf/defaults.ini'
    config = configparser.ConfigParser()

    config.read(ini_file)

    config.set('server', 'domain', ccvm[0])

    with open(ini_file, 'w') as f:
        config.write(f)


def configDS(scvm=None):  # 기본값 None 추가
    conn = sqlite3.connect(
        "/usr/share/ablestack/ablestack-wall/grafana/data/grafana.db")

    ds_update_query1 = "UPDATE data_source SET url = \'http://" + \
        "localhost:3001' WHERE name = 'Wall'"

    if os_type == "ablestack-hci" and scvm is not None:
        ds_update_query2 = "UPDATE data_source SET url = \'http://" + \
            gluePrometheusConfig(scvm)[0] + "' WHERE name = 'Glue'"

        glue_prometheus_ip = findGluePrometheusIp()
        if glue_prometheus_ip != "":
            ds_update_query2 = "UPDATE data_source SET url = \'http://" + \
                glue_prometheus_ip + glue_prometheus_port + "' WHERE name = 'Glue'"

    ds_update_query3 = "UPDATE data_source SET url = \'" + \
        "localhost:3306' WHERE name = 'Mold'"

    ds_update_query4 = "UPDATE data_source SET url = \'http://" + \
        "localhost:3001' WHERE name = 'Wall' AND org_id = '2'"

    ds_update_query5 = "UPDATE data_source SET url = \'http://" + \
            "127.0.0.1:3000' WHERE name = 'yesoreyeram-infinity-datasource'"

    cur = conn.cursor()
    cur.execute(ds_update_query1)
    if os_type == "ablestack-hci" and scvm is not None:
        cur.execute(ds_update_query2)
    cur.execute(ds_update_query3)
    cur.execute(ds_update_query4)
    cur.execute(ds_update_query5)

    conn.commit()
    conn.close()

    exist_yn = os.system("crontab -l |grep 'config_wall.py glueDsUpdate' > /dev/null")
    if exist_yn != 0:
        os.system("echo -e \'*/5 * * * * /usr/bin/python3 /usr/share/ablestack/ablestack-wall/python/config_wall.py glueDsUpdate \' >> /var/spool/cron/root")


def configSkydiveLink(ccvm):
    ccvm_ip = ccvm[0] if isinstance(ccvm, (list, tuple)) else str(ccvm)

    conn = sqlite3.connect("/usr/share/ablestack/ablestack-wall/grafana/data/grafana.db")
    cur = conn.cursor()

    cur.execute("SELECT id, data FROM dashboard WHERE org_id = 1 AND data LIKE '%:8082%'")
    rows = cur.fetchall()

    pattern = re.compile(r"http://(?:\d{1,3}\.){3}\d{1,3}:8082(?P<tail>[^\"'\s]*)")
    replacement = rf"http://{ccvm_ip}:8082\g<tail>"

    updated = 0
    for id_, data in rows:
        if data is None:
            continue
        # bytes → str 디코딩
        if isinstance(data, (bytes, bytearray)):
            try:
                data_str = data.decode('utf-8')
            except UnicodeDecodeError:
                data_str = data.decode('latin-1', 'ignore')  # 최후의 보루
        else:
            data_str = data

        new_data, count = pattern.subn(replacement, data_str)
        if count > 0 and new_data != data_str:
            cur.execute("UPDATE dashboard SET data = ? WHERE id = ?", (new_data, id_))
            updated += 1

    conn.commit()
    conn.close()


def configMoldUserDashboard():
    # grafana.db 에서 사용자 대시보드의 UID 가져오기
    conn = sqlite3.connect(
        "/usr/share/ablestack/ablestack-wall/grafana/data/grafana.db")

    user_dashboard_query = "SELECT uid, slug FROM dashboard WHERE title = '가상머신 상세 현황' AND org_id = 1"

    cur = conn.cursor()
    cur.execute(user_dashboard_query)
    user_dashboard_val = cur.fetchone()

    uri_val = '/d/' + user_dashboard_val[0] + '/' + user_dashboard_val[1] + '?kiosk'

    conn.close()

    cloud_db = pymysql.connect(
        user='cloud',
        password='Ablecloud1!',
        host='localhost',
        db='cloud',
        charset='utf8'
    )

    cursor = cloud_db.cursor(pymysql.cursors.DictCursor)
    update_user_dashboard_uri_sql = "UPDATE configuration SET value ='" + uri_val + "' WHERE name = 'monitoring.wall.portal.vm.uri'"
    cursor.execute(update_user_dashboard_uri_sql)

    cloud_db.commit()
    cloud_db.close()

# DB 파일 초기화 (기존 초기 파일로 되돌리기)
def initDB():
    if os_type == "ablestack-hci":
        cp("-f", "/usr/share/ablestack/ablestack-wall/grafana/data/grafana_org.db",
        "/usr/share/ablestack/ablestack-wall/grafana/data/grafana.db")
    else:
        cp("-f", "/usr/share/ablestack/ablestack-wall/grafana/data/grafana_gfs.db",
        "/usr/share/ablestack/ablestack-wall/grafana/data/grafana.db")

    # WAL/SHM 파일 제거
    try:
        os.remove("/usr/share/ablestack/ablestack-wall/grafana/data/grafana.db-wal")
    except FileNotFoundError:
        pass
    try:
        os.remove("/usr/share/ablestack/ablestack-wall/grafana/data/grafana.db-shm")
    except FileNotFoundError:
        pass

def gluePrometheusIpUpdate():
    conn = sqlite3.connect(
            "/usr/share/ablestack/ablestack-wall/grafana/data/grafana.db")

    select_query = "select url from data_source WHERE name = 'Glue'"

    cur = conn.cursor()
    cur.execute(select_query)

    crrent_ip = cur.fetchone()
    if crrent_ip is not None:
        glue_prometheus_ip = findGluePrometheusIp()
        if glue_prometheus_ip != "" and glue_prometheus_ip not in crrent_ip[0]:
            glue_ds_update_query = "UPDATE data_source SET url = \'http://" + \
                glue_prometheus_ip+glue_prometheus_port + "' WHERE name = 'Glue'"

            cur.execute(glue_ds_update_query)

    conn.commit()
    conn.close()

def findGluePrometheusIp():
    return ssh('-o', 'StrictHostKeyChecking=no', '-o', 'ConnectTimeout=5', "ablecube", "grep $(ceph orch ps |grep prometheus | awk '{print $2 \"-mngt\"}') /etc/hosts | awk '{print $1}'" ).strip()

def main():
    args = parseArgs()
    if (args.action) == 'config':
        if os_type == "ablestack-hci":
            try:
                os.system("rm -rf cd /nfs/prometheus/ > /dev/null")
                initDB()
                configYaml(args.cube, args.ccvm, args.scvm)
                configIni(args.ccvm)
                configDS(args.scvm)
                configSkydiveLink(args.ccvm)
                configMoldUserDashboard()

                json.loads(sh.python3("/usr/share/ablestack/ablestack-wall/python/config_loki.py","config", "--ccvm", args.ccvm, "--cube", args.cube, "--scvm", args.scvm))

                ret = createReturn(code=200, val="success wall configuration")
                print(json.dumps(json.loads(ret), indent=4))
            except Exception as e:
                ret = createReturn(code=500, val="fail to configuration")
                print(json.dumps(json.loads(ret), indent=4))
        else:
            try:
                initDB()
                configYaml(args.cube, args.ccvm)
                configIni(args.ccvm)
                configDS()
                configSkydiveLink(args.ccvm)
                configMoldUserDashboard()

                json.loads(sh.python3("/usr/share/ablestack/ablestack-wall/python/config_loki.py","config", "--ccvm", args.ccvm, "--cube", args.cube))

                systemctl('stop', "grafana-server")

                systemctl('enable', '--now', "blackbox-exporter")
                systemctl('enable', '--now', "node-exporter")
                systemctl('enable', '--now', "grafana-server")
                systemctl('enable', '--now', "prometheus")
                systemctl('enable', '--now', "process-exporter")
                systemctl('enable', '--now', "netdive-analyzer")

                systemctl('restart', "grafana-server")
                systemctl('restart', '--now', "prometheus")

                ret = createReturn(code=200, val="success wall configuration")
                print(json.dumps(json.loads(ret), indent=4))
            except Exception as e:
                ret = createReturn(code=500, val="fail to configuration")
                print(json.dumps(json.loads(ret), indent=4))
    if (args.action) == 'update':
        try:
            configYaml(args.cube, args.ccvm, args.scvm)
            systemctl('restart', 'prometheus')
            netdive_result = json.loads(sh.python3("/usr/share/ablestack/ablestack-wall/python/config_netdive.py","config", "--ccvm", args.ccvm, "--cube", args.cube))
            if netdive_result["code"] == 200:
                if os_type == "ablestack-hci":
                    for scvm_ip in args.scvm:
                        result = ssh('-o','StrictHostKeyChecking=no','-o','ConnectTimeout=5', scvm_ip, '/usr/bin/ls /usr/share/ablestack/ablestack-wall/process-exporter/').splitlines()
                        if 'scvm_process.yml' in result:
                            ssh('-o', 'StrictHostKeyChecking=no', '-o', 'ConnectTimeout=5', scvm_ip, "\cp -f /usr/share/ablestack/ablestack-wall/process-exporter/scvm_process.yml /usr/share/ablestack/ablestack-wall/process-exporter/process.yml").strip()
                            ssh('-o', 'StrictHostKeyChecking=no', '-o', 'ConnectTimeout=5', scvm_ip, "systemctl enable --now node-exporter.service process-exporter.service").strip()
                            ssh('-o', 'StrictHostKeyChecking=no', '-o', 'ConnectTimeout=5', scvm_ip, "rm -f /usr/share/ablestack/ablestack-wall/process-exporter/scvm_process.yml").strip()
            else:
                createReturn(code=500, val="fail to update netdive : " + netdive_result["val"])

            ret = createReturn(code=200, val="success prometheus update")
            print(json.dumps(json.loads(ret), indent=4))
        except Exception as e:
            ret = createReturn(code=500, val="fail to update prometheus : "+e)
            print(json.dumps(json.loads(ret), indent=4))
    if (args.action) == 'glueDsUpdate':
        try:
            gluePrometheusIpUpdate()
            ret = createReturn(code=200, val="success Glue prometheus ip update")
            print(json.dumps(json.loads(ret), indent=4))
        except Exception as e:
            ret = createReturn(code=500, val="fail to update Glue prometheus ip : "+e)
            print(json.dumps(json.loads(ret), indent=4))

    return ret

if __name__ == "__main__":
    main()
