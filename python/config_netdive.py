'''
Copyright (c) 2021 ABLECLOUD Co. Ltd
이 파일은 Wall VM을 구성할 때 netdive.yml 파일을 설정하는 프로그램입니다.
최초 작성일 : 2021. 09. 15
'''

# !/usr/bin/python3
# -*- coding: utf-8 -*-
import os
import yaml
import argparse
import json
import subprocess
from subprocess import call
from ablestack import *
from sh import systemctl

env=os.environ.copy()
env['LANG']="en_US.utf-8"
env['LANGUAGE']="en"

# netdive analyzer 포트
netdive_analyzer_port = ":8082"

'''
함수명 : parseArgs
이 함수는 python library argparse를 시용하여 함수를 실행될 때 필요한 파라미터를 입력받고 파싱하는 역할을 수행합니다.
예를들어 action을 요청하면 해당 action일 때 요구되는 파라미터를 입력받고 해당 코드를 수행합니다.
'''


def parseArgs():
    parser = argparse.ArgumentParser(description='Netdive Yaml file parsing and replace targets',
                                     epilog='copyrightⓒ 2021 All rights reserved by ABLECLOUD™')

    parser.add_argument('action', choices=[
        'config'], help='choose one of the actions')
    parser.add_argument('--cube', metavar='name', type=str,
                        nargs='*', help='cube ips')
    parser.add_argument('--ccvm', metavar='name', type=str,
                        nargs='*', help='ccvm ips')

    return parser.parse_args()


def ccvmNetdiveConfig(ccvm_ip):
    ccvm = ccvm_ip.copy()
    for i in range(len(ccvm)):
        ccvm[i] = ccvm[i] + netdive_analyzer_port
    return ccvm


def cubeServiceConfig(cube_ip):
    cube = cube_ip.copy()
    for i in range(len(cube)):
        cube[i] = cube[i]
    return cube


# 함수명 : configYaml
# 주요 기능 : 입력 받은 ip를 netdive.yml 파일의 analyzers에 설정


def configYaml(ccvm):
    netdive_yml_path = '/usr/share/ablestack/ablestack-netdive/'

    with open(netdive_yml_path + "netdive.yml") as f:
        netdive_org = yaml.safe_load(f)

        netdive_org['analyzers'] = ccvmNetdiveConfig(ccvm)

        with open(netdive_yml_path + "netdive.yml", 'w') as yaml_file:
            yaml_file.write(
                yaml.dump(netdive_org, default_flow_style=False))


# 함수명 : SendCommandToHost
# 주요 기능 : 입력 받은 cube ip의 주소로 netdive.yml 파일을 전송하고 service를 재시작 합니다.

def SendCommandToHost(cube):
    netdive_yml_path = '/usr/share/ablestack/ablestack-netdive/'
    result_code_list = []
    # 오류 발생할 경우, 3번 재시도 합니다.
    tries = 3
    for j in range(tries):
        for i in range(len(cube)):
            stringCube = ''.join(cubeServiceConfig(cube)[i])
            rc_one = call(["scp -q -o StrictHostKeyChecking=no " + netdive_yml_path + "netdive.yml root@" + stringCube + ":" + netdive_yml_path], universal_newlines=True, shell=True, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT)
            rc_two = call(["ssh -o StrictHostKeyChecking=no root@" + stringCube + " 'systemctl restart netdive-agent.service'"], universal_newlines=True, shell=True, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT)
            result_code_list.append(rc_one)
            result_code_list.append(rc_two)
        if all(0 == x for x in result_code_list):
            result = 200
        else:
            result = 500
        if result == 500:
            if j < tries - 1:
                continue
            else:
                raise
        break
    return result


def main():
    args = parseArgs()

    if (args.action) == 'config':
        try:
            configYaml(args.ccvm)
            systemctl('enable', '--now', "netdive-analyzer")
            systemctl('restart', "netdive-analyzer")
            result = SendCommandToHost(args.cube)
            if result == 200:
                ret = createReturn(code=200, val="update netdive configuration")
                print(json.dumps(json.loads(ret), indent=4))
            else:
                ret = createReturn(code=500, val="fail to update netdive configuration")
                print(json.dumps(json.loads(ret), indent=4))
        except Exception as e:
            ret = createReturn(code=500, val="fail to update netdive configuration")
            print(json.dumps(json.loads(ret), indent=4))
    return ret


if __name__ == "__main__":
    main()
