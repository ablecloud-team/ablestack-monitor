'''
Copyright (c) 2021 ABLECLOUD Co. Ltd
이 파일은 notification channel의 수신 이메일을 업데이트 하는 프로그램입니다.
최초 작성일 : 2021. 08. 25
'''

#!/usr/bin/python3
# -*- coding: utf-8 -*-

import requests
import json
import sys
from ablestack import *


'''
함수명 : updateNotification
이 함수는 notification.json 파일을 이용하여 기존의 notification channel을 업데이트 합니다.
기존의 notification channel이 정상적으로 업데이트 되기 위해서는 먼저 update_noti_json.py 프로그램을 실행하여 최신의 notification.json 파일을 생성해야 합니다.
'''


def updateNotification():
    with open("../properties/api.key") as apikey:
        key = apikey.readline()

    wall_ip = sys.argv[1]
    url = 'http://admin:password@' + wall_ip + \
        ':3000/api/alert-notifications/uid/ablecloud-admin'

    headers = {'Accept': 'application/json', 'Content-Type': 'application/json',
               'Authorization': 'Bearer' + key}

    with open("/usr/share/ablestack/ablestack-wall/properties/notification.json", "r") as notificationJsonFile:
        data = json.load(notificationJsonFile)

    res = requests.put(url, data=json.dumps(data), headers=headers)
    # print(str(res.status_code) + "|" + res.text)
    ret = createReturn(code=res.status_code, val=res.text)
    print(json.dumps(json.loads(ret), indent=4))
    return ret


def main():
    if len(sys.argv) == 2:
        try:
            updateNotification()
        except Exception as e:
            print('Update failed', e)

    else:
        print("One parameter(wallvm_ip) is required.")
        sys.exit()


if __name__ == "__main__":
    main()
