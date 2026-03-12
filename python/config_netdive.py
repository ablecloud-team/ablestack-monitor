'''
Copyright (c) 2021 ABLECLOUD Co. Ltd
이 파일은 Wall VM을 구성할 때 Netdive 서비스를 기동하는 프로그램입니다.
최초 작성일 : 2021. 09. 15
수정일 : 2026. 03. 11
수정 내용 : Netdive 설정 파일 배포 기능을 제거하고, ccvm에서 netdive-analyzer를 먼저 기동한 뒤
            cube 호스트에서 netdive-agent 서비스를 재시작하는 방식으로 변경하였습니다.
'''

#!/usr/bin/python3
# -*- coding: utf-8 -*-

import os
import argparse
import json
import subprocess
from subprocess import call
from ablestack import *
from sh import systemctl
import sys

env = os.environ.copy()
env['LANG'] = "en_US.utf-8"
env['LANGUAGE'] = "en"

# SSH 대기 방지용 공통 옵션입니다.
SSH_COMMON_OPTS = "-o BatchMode=yes -o StrictHostKeyChecking=no -o ConnectTimeout=5"


'''
함수명 : parseArgs
주요 기능 : 실행 시 필요한 파라미터를 입력받고 파싱합니다.
'''


def parseArgs():
    parser = argparse.ArgumentParser(
        description='Start netdive analyzer on ccvm and netdive agent on cubes',
        epilog='copyrightⓒ 2021 All rights reserved by ABLECLOUD™'
    )

    parser.add_argument('action', choices=['config'], help='choose one of the actions')
    parser.add_argument('--cube', metavar='name', type=str, nargs='*', help='cube ips')
    parser.add_argument('--ccvm', metavar='name', type=str, nargs='*', help='ccvm ips')

    return parser.parse_args()


def cubeServiceConfig(cube_ip):
    cube = cube_ip.copy()
    for i in range(len(cube)):
        cube[i] = cube[i]
    return cube


'''
함수명 : StartAnalyzer
주요 기능 : ccvm에서 netdive-analyzer를 먼저 enable/start 합니다.
'''


def StartAnalyzer():
    try:
        # 1. 먼저 활성화(enable)를 시도합니다. (--now 없이 등록만)
        systemctl("enable", "netdive-analyzer")

        # 2. 이미 실행 중이든 아니든 'restart'를 통해 깨끗하게 다시 시작합니다.
        # 이렇게 하면 'activating' 상태에서 멈춘 경우도 해결됩니다.
        sys.stderr.write(">> [INFO] Restarting netdive-analyzer service...\n")
        systemctl("restart", "netdive-analyzer")

    except Exception as e:
        # 에러가 나더라도 로그만 찍고 스크립트가 완전히 죽지 않게 예외 처리를 보강합니다.
        sys.stderr.write(f">> [WARNING] Analyzer service issue: {str(e)}\n")
        # 만약 restart 실패가 치명적이라면 여기서 raise e를 하여 중단시킬 수 있습니다.


'''
함수명 : SendCommandToHost
주요 기능 : 입력 받은 cube ip의 주소에서 netdive-agent.service를 기동(재시작)합니다.
'''


def SendCommandToHost(cube):
    result_code_list = []
    tries = 3

    for j in range(tries):
        result_code_list = []
        for i in range(len(cube)):
            stringCube = cube[i]

            # SSH 명령 실행
            ssh_cmd = (
                    "ssh " + SSH_COMMON_OPTS + " root@" + stringCube +
                    " 'systemctl reset-failed netdive-agent.service || true; "
                    "systemctl restart netdive-agent.service'"
            )

            # stdout은 버리되, 에러 발생 시 확인을 위해 stderr는 유지하거나 별도로 찍을 수 있게 함
            rc = call(
                [ssh_cmd],
                universal_newlines=True,
                shell=True,
                env=env
            )
            result_code_list.append(rc)

            # 실패 시 stderr로 즉시 출력 (JSON 응답에 포함되지 않음)
            if rc != 0:
                sys.stderr.write(f">> [ERROR] Host {stringCube} failed with exit code {rc}\n")

        if all(0 == x for x in result_code_list):
            return 200

        if j < tries - 1:
            sys.stderr.write(f">> [RETRY] {j+1}/{tries} - Retrying failed hosts...\n")
            continue

    return 500


def main():
    args = parseArgs()
    # 기본 리턴값 설정 (기존 형식 유지)
    ret = createReturn(code=500, val="fail to update netdive configuration")

    if args.action == 'config':
        try:
            # 1) ccvm에서 analyzer를 먼저 기동
            StartAnalyzer()

            # 2) cube들에서 agent를 기동
            result = SendCommandToHost(args.cube)

            if result == 200:
                ret = createReturn(code=200, val="update netdive configuration")
            else:
                # 실패 시 구체적인 이유는 stderr로 찍고 JSON은 기존 메시지 유지
                sys.stderr.write(">> [FATAL] Netdive configuration failed on one or more nodes.\n")
                ret = createReturn(code=500, val="fail to update netdive configuration")

        except Exception as e:
            # 예외 발생 시 상세 내용을 stderr에 출력
            sys.stderr.write(f">> [EXCEPTION] {str(e)}\n")
            ret = createReturn(code=500, val="fail to update netdive configuration")

        # 최종 JSON 결과 출력
        print(json.dumps(json.loads(ret), indent=4))
        return ret


if __name__ == "__main__":
    main()