# PC Control Hub Agent 설계 초안 (v1.1.0 이후, 별도 프로젝트)

작성 2026-09-17. 상태: **초안**. v1.1.0(Edge 드라이버) 완료 후 기획을 확정한다.

## 1. 배경과 원칙

- PC 서비스(이 레포)의 텔레그램 기능은 **"봇 하나 = PC 하나"** 스탠드얼론으로 유지한다.
  여러 PC를 한 봇으로 묶는 로직은 PC 서비스에 넣지 않는다.
- Edge 드라이버는 텔레그램과 무관하다. SmartThings ↔ PC 서비스 `/st/v1`만 담당한다.
- 여러 PC를 하나의 텔레그램 봇으로 다루는 일은 **홈랩 서버의 Docker 컨테이너 "허브 에이전트"** 가 맡는다.
  허브 에이전트는 Edge 드라이버와 똑같이 `/st/v1` 클라이언트일 뿐이다.

```
                 ┌────────────┐  /st/v1 (poll + push)   ┌──────────────┐
 SmartThings ───▶│ Edge 드라이버│────────────────────────▶│ PC 서비스 #1  │
                 └────────────┘                          │ (Go, Windows)│
                 ┌────────────┐  /st/v1 (poll + push)   ├──────────────┤
 Telegram   ◀──▶│ 허브 에이전트 │────────────────────────▶│ PC 서비스 #2  │
 (봇 하나)       │ (Docker)    │  SSDP 검색, WoL          ├──────────────┤
                 └────────────┘                          │ PC 서비스 #N  │
                                                         └──────────────┘
```

`/st/v1`이 PC 서비스의 **유일한 통합 API**가 된다. 이후 Home Assistant·MQTT 같은 연동도 같은 문으로 들어온다.

## 2. 목표

1. 봇 하나로 LAN의 PC 여러 대 상태 조회·전원 명령·예약·취소·WoL.
2. PC 이벤트(전원·예약·보안·시스템)를 PC 이름 머리말과 함께 한 채팅으로 모아 받기. 조용한 시간대·요약 지원.
3. PC 자동 발견(SSDP) + 정적 설정 병행. IP 변화(DHCP) 추적.
4. 설정 파일 하나로 배포. 컨테이너 재시작에도 상태 유지.
5. PC 서비스 쪽 변경 없이 동작(v1.1.0 `/st/v1` 프로토콜 1 기준).

비목표: PC 서비스 텔레그램 기능 대체(공존한다. 봇이 다르면 충돌 없음), Web UI(최소 `/health`만), 클라우드 릴레이.

## 3. 형태

- **언어/런타임**: Go, 단일 바이너리, 멀티아치 Docker 이미지(ghcr.io/Protomothis/pc-control-hub). aniflux와 같은 태그→Actions→ghcr→compose pull 경로.
- **네트워크**: `network_mode: host` 권장. 이유 ①SSDP 멀티캐스트 ②WoL 브로드캐스트 ③푸시 콜백 주소가 컨테이너 IP가 아닌 호스트 IP여야 함.
  브리지 모드도 지원하되 `advertise_addr`(콜백 호스트)·정적 PC 목록 필수, WoL은 directed broadcast로만.
- **설정**: `/config/hub.yaml`(아래), 시크릿은 파일 권한 0600 또는 env(`HUB_TELEGRAM_TOKEN`, `HUB_PC_<NAME>_SECRET`). 상태는 `/data/state.json`(오프셋, 구독 id, 마지막 상태).
- **레포**: 별도 레포 `pc-control-hub`. 이 레포에서 공유하는 것:
  - `pkg/stclient`(신설): `/st/v1` Go 클라이언트(status/command/schedule/subscribe/description) + SSDP 검색 + 푸시 본문 타입. Edge 드라이버가 쓰는 프로토콜과 1:1. **`internal/`은 import 불가**하므로 `pkg/`에 둔다.
  - `service/telegram`(기존): Bot API 클라이언트·렌더러·폴러. 허브용 템플릿은 허브 레포가 가진다(#75의 PC 이름 머리말 렌더러 옵션 재사용).
  - `service/notify`(기존): 필터·조용한 시간대·집계·스로틀 파이프라인. 허브는 PC별 Sink 대신 "PC 태그가 붙은 이벤트"를 한 버스로 흘린다.

## 4. 설정 파일 예시

```yaml
telegram:
  token_env: HUB_TELEGRAM_TOKEN
  allowed_chat_ids: ["123456789"]
  language: ko
  quiet_hours: { enabled: true, start: "23:00", end: "07:00", digest: true }
discovery:
  ssdp: true
  interval: 5m
  follow_ip: true
pcs:
  - name: 데스크탑        # 메시지 머리말·명령 대상 이름
    machine_id: "9f3c-..."  # SSDP로 채움. 정적일 때만 직접 기입
    host: 192.168.1.20      # 비우면 검색 IP 사용
    port: 5001
    secret_env: HUB_PC_DESKTOP_SECRET
    mac: "AA:BB:CC:DD:EE:FF"
    wol_broadcast: 255.255.255.255
    notify: { remote: true, schedule: true, power: true, security: true, system: true }
  - name: 서재
    ...
push:
  listen: ":41234"
  advertise_addr: ""        # host 모드면 비움(자동)
```

## 5. 텔레그램 UX

- `/pcs` : PC 목록과 상태 한 줄씩(🟢 on · 💤 절전 · ⚫ off · ⏳ 종료 대기 4:12).
- `/status <pc>` `/shutdown <pc> [분]` `/restart <pc>` `/sleep <pc>` `/hibernate <pc>` `/lock <pc>` `/screenoff|/screenon <pc>` `/wake <pc>` `/cancel <pc>`.
  `<pc>`는 이름·번호·접두 일치. PC가 한 대면 생략 가능.
- `/menu` : 인라인 키보드 [PC 선택] → PC별 메뉴(PC 서비스의 `/menu`와 같은 버튼 구성 + [깨우기]) → 확인 단계.
- `/all status` `/all shutdown 10` : 그룹 명령(확인 필수). 실패한 PC만 따로 보고.
- 알림: 머리말 `🖥 <PC 이름>` + PC 서비스와 같은 템플릿 톤. 유예 알림의 [취소]/[지금 실행] 버튼은 해당 PC로 라우팅.
- 오프라인 PC에 명령 → "꺼져 있음. [깨우기]" 버튼 제시. 깨운 뒤 명령을 이어서 하는 "wake-then-run"은 M2.

## 6. 동작

- **폴링 + 푸시**: PC별 `pollInterval`(기본 60s, 푸시 구독 성공 시 5m). 구독은 PC별, TTL 80% 갱신. 푸시 최상위 `machine_id`로 라우팅(`edge-driver.md` §6.7의 다중 PC 규칙과 동일).
- **전원 상태 머신**: Edge 드라이버 `state.lua`와 같은 전이 규칙을 Go로 구현(`pkg/stclient/state.go`로 공유 가능하면 공유).
- **발견**: 주기 SSDP + `description` 조회. `machine_id` 기준 중복 방지, `follow_ip`면 IP 갱신. 새 PC 발견 시 알림 "새 PC 발견: DESKTOP-ABC (192.168.1.31). 시크릿을 설정에 추가하세요".
- **WoL**: 매직 패킷 3회, 포트 7·9. 성공 여부는 이후 status 성공으로 판단(90s).
- **보안**: allowed_chat_ids 필수(비면 기동 거부), 시크릿은 헤더, 콜백 리스너는 LAN 인터페이스에만 바인드, 요청 출처가 등록된 PC IP인지 검증.
- **가용성**: PC 서비스 재시작 → 구독 소실 → 폴링 실패 시 재구독. 허브 재시작 → state.json의 구독 id는 버리고 새로 구독.

## 7. PC 서비스 쪽 후속(작음, v1.1.x)

- `#70`의 "허브 연결" 표시가 User-Agent를 파싱한다. 허브 에이전트 UA `pc-control-hub/<ver>`도 이름과 함께 표시되게 한다(현재는 버전만).
- `pkg/stclient` 추출 + 골든 테스트(status JSON ↔ 타입).
- 문서: "여러 PC" 절에서 텔레그램은 허브 에이전트로 안내.

## 8. 단계

| 단계 | 내용 |
|---|---|
| M1 | 레포·이미지·compose, hub.yaml, `pkg/stclient`, PC별 폴링/푸시, `/pcs` `/status` `/shutdown …` `/cancel`, 알림 머리말, allowed chats |
| M2 | SSDP 발견·IP 추적, WoL·`/wake`, `/menu` 인라인 UX, wake-then-run, 조용한 시간대·요약 |
| M3 | `/all` 그룹 명령, Home Assistant(MQTT discovery) 브리지 검토, 헬스체크·메트릭 |

## 9. 열린 결정

- 레포 이름: `pc-control-hub`(가칭). 개명 안 한 PC 서비스 이름과의 관계를 README에서 설명.
- 채팅 구조: 봇 하나·채팅 하나에 모든 PC(기본) vs PC별 채팅 지정 옵션(`pcs[].chat_id`).
- 허브가 관리하는 PC의 자체 텔레그램 제어를 끄도록 권고할지(중복 알림 방지). 기본 권고: 허브가 있으면 PC 서비스 텔레그램은 끔.
- 홈랩 배포 위치: 192.168.1.50 Docker 서버(host 모드 가능 여부 확인 필요).
