# license-exporter

ABLESTACK host license API 상태를 Prometheus metric으로 노출하는 경량 exporter입니다.

## 제안 디렉터리 구조

실 배포 경로 기준:

```text
/usr/share/ablestack/ablestack-wall/
└── license-exporter/
    ├── license_exporter.py
    ├── README.md
    └── license-exporter.env.example
```

레포 반영 기준:

```text
exporters/license-exporter/
systemds/host/license-exporter.service
sysconfig/license-exporter
prometheus/license-exporter.scrape.yml.example
```

## 동작 모드

- `host_api`
  - `https://127.0.0.1:8080/api/v1/license/isLicenseExpired`
  - 각 호스트에서 자기 자신의 license API를 호출
  - self-signed TLS 환경을 위해 기본적으로 인증서 검증을 건너뜀
  - 원본 API가 날짜를 내려줄 때 `days_until_expiry` 계산 가능

## 노출 metric

- `ablestack_license_api_up`
- `ablestack_license_expired`
- `ablestack_license_has_license`
- `ablestack_license_valid`
- `ablestack_license_api_http_status`
- `ablestack_license_scrape_duration_seconds`
- `ablestack_license_last_scrape_timestamp_seconds`
- `ablestack_license_expiry_timestamp_seconds`
- `ablestack_license_issued_timestamp_seconds`
- `ablestack_license_days_until_expiry`
- `ablestack_license_exporter_error_info`

## 실행 예시

```bash
LICENSE_EXPORTER_SOURCE=host_api \
LICENSE_EXPORTER_API_URL=https://127.0.0.1:8080/api/v1/license/isLicenseExpired \
/usr/bin/python3 /usr/share/ablestack/ablestack-wall/license-exporter/license_exporter.py
```

기본 listen 포트는 `3007`, metrics path는 `/metrics` 입니다.
