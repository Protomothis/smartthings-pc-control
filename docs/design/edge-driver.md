# SmartThings Edge 드라이버 설계 (v1.1.0)

작성 2026-09-17. 이 문서는 마일스톤 **v1.1.0 "SmartThings Edge driver"** 의 구현 계약이다.
서비스(Go)와 드라이버(Lua)는 이 문서의 프로토콜을 기준으로 병렬로 구현하며,
문서와 코드가 어긋나면 문서를 먼저 고친다.

## 1. 배경과 목표

기존 [PCControl Edge 드라이버](https://github.com/toddaustin07/PCControl)는
스위치 하나(on = WoL, off = 선택한 종료 명령)와 주기적 ping 만 제공한다.
우리 서비스가 가진 유예·예약·취소·출처·전원 이벤트·WoL 진단은 SmartThings 쪽에
전혀 드러나지 않는다. 직접 만든 드라이버의 목표는 다음이다.

1. **정확한 상태**: on / 절전 / 최대절전 / 꺼짐 / 깨우는 중 / 종료 대기를 구분하고,
   서비스가 전원 이벤트를 허브로 푸시해 폴링 지연 없이 반영한다.
2. **유예·예약이 보이는 장치**: 종료 대기 카운트다운, 출처, 취소 버튼, 프리셋 예약.
3. **조용한 실패 제거**: 시크릿 불일치·연결 불가·WoL 비활성 어댑터·버전 비호환을
   앱 속성으로 보여 준다. PC 쪽 GUI에도 허브 연결 상태를 보여 준다.
4. **자동화 표현력**: 명령에 유예 모드와 분 단위 예약을 인자로 받고, 상태 변화를
   Routine 조건으로 쓸 수 있게 한다.
5. **새 기능**: 디스플레이 켜기/끄기 자식 장치, 세션 정보(잠금·유휴, 선택 사항).
6. **설치 경험**: SSDP 자동 검색, 한국어·영어, GUI에서 시크릿 생성·연결 확인.
7. **보안**: 시크릿을 URL 경로가 아닌 헤더로, 허브 IP 허용 목록, 콜백 주소 검증.

PCControl 호환 경로(`/{secret}/{command}`)는 그대로 유지한다. 기존 드라이버
사용자는 아무것도 바꾸지 않아도 된다.

## 2. 범위와 단계

| 단계 | 서비스 | 드라이버 | 결과 |
|---|---|---|---|
| **P1 MVP** | `/st/v1/` 프로토콜(상태·명령·예약·취소), origin `smartthings`, `turnscreenon`, 헤더 인증, 허브 허용 목록 | 프로필·환경설정·수동 추가·스위치/WoL/ping·healthCheck, 커스텀 capability(상태·명령·예약), 오류/호환 표시, 테스트 하네스 | PCControl 동등 + 유예/예약/취소/상태 |
| **P2 푸시** | `/st/v1/subscribe`, notify 버스 SmartThings Sink, 전원·세션·디스플레이 이벤트 | 허브 내 HTTP 리스너, 구독 갱신, 즉시 반영 | 폴링 5분으로 완화, 즉시 상태 |
| **P3 완성** | SSDP 응답기, GUI SmartThings 섹션, 세션 정보 옵트인 | SSDP 검색, 디스플레이 자식 장치, WoL 재시도/waking, ko/en, CI 패키징, 문서 | 채널 공개 |

모두 v1.1.0 마일스톤에 포함한다. 단계는 병렬 작업의 의존 순서일 뿐이다.

## 3. 저장소 구성

같은 레포의 `edge/` 폴더에 드라이버를 둔다. 프로토콜 스키마를 서비스와 함께
버전 관리하고, Actions 한 곳에서 패키징한다.

```
edge/
  config.yml                 # Edge driver 메타 (name, packageKey, permissions: lan)
  profiles/
    pc.yml                   # main 컴포넌트 프로필
    pc-display.yml           # 디스플레이 자식 장치 프로필
  capabilities/              # 커스텀 capability 정의/프레젠테이션 JSON (CLI로 생성)
    pcPowerState.json  pcPowerState.presentation.json
    pcCommand.json     pcCommand.presentation.json
    pcSchedule.json    pcSchedule.presentation.json
    pcStatus.json      pcStatus.presentation.json
    pcSession.json     pcSession.presentation.json
  src/
    init.lua                 # 드라이버 진입점, lifecycle
    caps.lua                 # 커스텀 capability id 상수 (NAMESPACE 한 곳)
    client.lua               # /st/v1 HTTP 클라이언트 (cosock http)
    discovery.lua            # SSDP M-SEARCH + 수동 추가
    poll.lua                 # 상태 폴링/health
    push.lua                 # 허브 리스너 + 구독 갱신
    wol.lua                  # 매직 패킷, waking 상태 머신
    state.lua                # status JSON → capability 이벤트 매핑
    display.lua              # 자식 장치
    i18n.lua                 # ko/en 문자열 (속성 문자열용)
  tests/
    run.lua                  # 테스트 러너 (assert 기반, 의존성 없음)
    mocks/st/...             # st.driver, st.capabilities, cosock 최소 목
    *_test.lua
  package.json               # devDependency fengari (Lua 5.3 인터프리터), scripts: test
  tools/lua.js               # fengari 기반 `lua <file>` 러너
  README.md                  # 설치·채널·개발 안내
.github/workflows/edge.yml   # Lua 테스트 + `edge-v*` 태그에서 CLI 패키징/채널 배포
```

로컬에는 Lua 인터프리터가 없다. **fengari(Lua 5.3 구현, JS)** 를 `edge/package.json`
devDependency로 두고 `tools/lua.js`로 실행한다. 이 세션에서는
`bun`(`C:\Users\streamdeck\AppData\Local\Kiro-Cli\bun.exe`)으로 실행했고, CI는
ubuntu에서 `npm ci && npm test`로 같은 테스트를 돈다. Edge 런타임도 Lua 5.3이다.

## 4. 서비스 프로토콜 `/st/v1` (메인 포트, 기본 5001)

### 4.1 인증

- 시크릿이 설정돼 있으면 모든 `/st/v1/*` 요청은 헤더 `X-PC-Secret: <secret>` 필요.
  누락·불일치 → `401` + `security.unauthorized` 이벤트(기존 집계 경로 재사용).
- `smartthings.allowed_hubs` (config, 문자열 배열)가 비어 있지 않으면 그 IP 외 출처는
  `403`. 기본 빈 배열(모두 허용). GUI에서 "현재 연결된 허브를 허용 목록에 추가" 제공.
- 시크릿이 비어 있으면 인증 없음(기존과 동일). 드라이버는 이 경우 `connection=ok`
  로 두되 `pcStatus.message`에 "no secret" 경고 문자열을 넣는다(`connection` enum에 `noSecret`은 두지 않는다).
- 레거시 `/{secret}/{command}`는 변경 없음.

### 4.2 `GET /st/v1/status`

```json
{
  "protocol": 1,
  "service_version": "v1.1.0",
  "machine_id": "9f3c...-machine-guid",
  "hostname": "DESKTOP-ABC",
  "power": "on",
  "uptime_seconds": 12345,
  "last_shutdown_clean": true,
  "secret_set": true,
  "grace": { "enabled": true, "seconds": 300 },
  "schedule": {
    "active": true, "command": "shutdown", "origin": "smartthings",
    "remaining_seconds": 240, "execute_at": "2026-09-17T23:10:00+09:00"
  },
  "last_command": { "command": "shutdown", "origin": "smartthings", "at": "2026-09-17T23:05:00+09:00" },
  "update": { "available": false, "latest": "v1.1.0" },
  "wol": { "ready": true, "adapters": [ { "name": "Ethernet", "mac": "AA:BB:CC:DD:EE:FF", "wol_enabled": true, "wol_capable": true } ] },
  "display": "on",
  "session": { "exposed": false }
}
```

- `power`는 이 엔드포인트가 응답한다는 사실 자체로 항상 `"on"`. 절전·종료 상태는
  푸시 이벤트와 ping 실패로 드라이버가 판단한다(§6.2).
- `schedule.origin`: `ui` / `remote` / `telegram` / `smartthings`.
- `session`이 옵트인(`smartthings.expose_session=true`)이면
  `{ "exposed": true, "locked": false, "idle_seconds": 1200, "user": "kim" }`.
  기본 꺼짐. `user`는 `expose_session_user=true`일 때만 포함.
- `display`: `on` / `off` / `unknown`. 마지막으로 서비스가 보낸 명령 기준.
- `last_shutdown_clean`: state.json에 `clean_shutdown` 플래그를 두고 `power.stopping`
  에서 true, 시작 시 읽은 뒤 false로 초기화. 비정상 종료(정전) 판별용.
- 서비스가 응답할 때마다 허브 출처(IP, User-Agent의 드라이버 버전, 시각)를
  `hubLastSeen`으로 기억한다. GUI SmartThings 섹션이 이를 표시한다.

### 4.3 `POST /st/v1/command`

```json
{ "command": "shutdown", "mode": "default", "minutes": 0 }
```

- `command`: `shutdown` `forceshutdown` `restart` `hibernate` `suspend` `lock`
  `turnscreenoff` `turnscreenon` `ping`.
- `mode`: `default`(설정된 유예 따름) / `immediate`(유예 없이) / `grace`(설정 유예 강제).
  `forceshutdown`은 항상 즉시. `minutes=0`에서 유예로 미뤄지는 경우 origin은 레거시 경로와 같이 `remote`를 유지한다(트레이 토스트·텔레그램 [지금 실행]/[취소] 메시지가 그 경로에 묶여 있다). `smartthings` origin은 `minutes > 0` 예약에만 쓴다.
- `minutes > 0` 이면 예약: `setSchedule(command, minutes, originSmartThings)`.
  기존 예약이 있으면 대체하고 `schedule.replaced`를 낸다(기존 정책 그대로).
- 응답 `200 { "accepted": true, "executed": false, "schedule": {...} }`.
  즉시 실행이면 `executed: true`, `schedule: {"active": false}`.
- 알 수 없는 명령 `400` + `security.unknown_command`.
- 새 명령 `turnscreenon`: `SendMessage(HWND_BROADCAST, WM_SYSCOMMAND, SC_MONITORPOWER, -1)`
  을 사용자 세션에서 실행. 레거시 경로에서도 `/{secret}/turnscreenon`으로 호출 가능.
  Telegram `/screenon`도 함께 추가한다(작은 변경, 같은 이슈).

### 4.4 `DELETE /st/v1/schedule`

`cancelScheduleBy("smartthings")`. 응답 `200 {"cancelled": true}` 또는 예약 없음
`200 {"cancelled": false}`. 취소 알림의 출처 라벨 `by_smartthings`("SmartThings").

### 4.5 `POST /st/v1/subscribe` (P2)

```json
{ "callback": "http://192.168.1.20:41234/pc/evt", "ttl_seconds": 600, "driver_version": "1.0.0" }
```

- 콜백 호스트는 **요청 출처 IP와 같아야** 하고 사설 대역이어야 한다. 아니면 `400`.
- 구독은 `ttl_seconds`(60~3600) 뒤 만료. 드라이버는 TTL의 80% 시점에 갱신.
- 응답 `200 { "id": "sub-1", "expires_at": "..." }`. `DELETE /st/v1/subscribe/{id}`로 해지.
- 구독은 메모리에만 둔다(서비스 재시작 시 드라이버가 폴링 실패→재구독).
- 서비스는 이벤트를 `POST callback`으로 보낸다. 본문:

```json
{ "protocol": 1, "type": "schedule.created", "at": "2026-09-17T23:05:00+09:00",
  "data": { "command": "shutdown", "origin": "smartthings", "remaining_seconds": 300 },
  "status": { ...GET /st/v1/status 와 동일... } }
```

  매 이벤트에 전체 `status`를 실어 드라이버가 diff 없이 갱신한다. 2초 타임아웃,
  실패 1회 재시도, 연속 3회 실패 시 구독 제거.
- 푸시 대상 이벤트: `power.stopping`(data.reason: shutdown/restart/suspend/hibernate/unknown),
  `power.started`, `power.resumed`, `schedule.*`, `remote.*`, `system.updated`,
  `system.update_available`, `display.changed`, `session.locked`/`session.unlocked`(옵트인).
- 구현은 `service/notify` 버스의 **새 Sink** (`service/st_push.go`)로 한다. 알림
  카테고리 필터·조용한 시간대는 적용하지 않는다(장치 상태는 알림이 아니다).
  `power.stopping`은 종료 직전이므로 동기 전송(최대 1.5초 대기) 후 계속 종료한다.

### 4.6 SSDP (P3)

- UDP 239.255.255.250:1900 `M-SEARCH` 중 `ST: urn:smartthings-pc-control:device:pc:1`
  (또는 `ssdp:all`)에 응답. `LOCATION: http://<ip>:<port>/st/v1/description`,
  `USN: uuid:<machine_id>::urn:smartthings-pc-control:device:pc:1`.
- `GET /st/v1/description`은 인증 없이 `{ "protocol":1, "machine_id":..., "hostname":..., "service_version":..., "port": 5001, "secret_set": true }`만 반환.
- config `smartthings.discovery` 기본 true, GUI에서 토글.

### 4.7 config.json 추가

```json
"smartthings": {
  "discovery": true,
  "allowed_hubs": [],
  "expose_session": false,
  "expose_session_user": false
}
```

핫 리로드 대상. `/api/config`로 GUI가 읽고 쓴다.

## 5. 드라이버 장치 모델

### 5.1 프로필 `pc.yml` (main)

| capability | 용도 |
|---|---|
| `switch` | on → WoL 시퀀스, off → 환경설정의 기본 off 명령(`mode=default`) |
| `healthCheck` | 폴링/푸시 기반 online/offline |
| `{NS}.pcPowerState` | `powerState` enum: `on` `sleeping` `hibernated` `off` `waking` `shuttingDown` `unknown` |
| `{NS}.pcCommand` | command `execute(command, mode, minutes)`; attr `lastCommand` string("shutdown · SmartThings · 23:05") |
| `{NS}.pcSchedule` | attrs `active` bool, `command` string, `remainingSeconds` integer, `executeAt` string(로컬 `HH:MM`), `origin` string; command `cancel()` ; command `schedule(command, minutes)` — `minutes`는 capability 상으로는 integer 1..1440(서비스 상한과 동일)이고, 프리셋 5/15/30/60/120은 프레젠테이션의 선택지로만 제공한다 |
| `{NS}.pcStatus` | attrs `connection` enum(`ok` `unauthorized` `unreachable` `incompatible`), `serviceVersion` string, `updateAvailable` bool, `wolReady` bool, `lastSeen` string(마지막 성공 폴링의 로컬 `HH:MM:SS`), `message` string(사람이 읽는 오류/안내 **한 줄**) |
| `{NS}.pcSession` | attrs `locked` bool, `idleMinutes` integer, `user` string — `session.exposed=false`면 드라이버가 이 capability의 이벤트를 아예 내보내지 않아 마지막 값이 유지된다. 프레젠테이션 `visibleCondition`으로 숨기는 것은 장치 프레젠테이션 몫이므로 #74로 미룬다 |
| `refresh` | 즉시 폴링 |

### 5.2 자식 장치 `pc-display.yml`

`switch` 하나. on → `turnscreenon`, off → `turnscreenoff`. 상태는 `status.display`.
환경설정 `createDisplayDevice`(기본 true)로 생성/제거.

### 5.3 프레젠테이션

- 대시보드: `switch` + `pcPowerState.powerState` 상태 문구.
- 상세: 스위치, 전원 상태, 명령 실행(enum 선택 + 모드 + 분), 예약 카드(남은 시간, 출처, 취소),
  상태 카드(연결, 버전, 업데이트, WoL 준비, 마지막 확인, 메시지), 세션(조건부).
- 자동화: 조건 `powerState`, `pcSchedule.active`, `connection`, `locked`;
  액션 `pcCommand.execute`, `pcSchedule.cancel`, `pcSchedule.schedule`, `switch`.

### 5.4 환경설정(preferences)

`ipAddress`, `port`(5001), `secret`(string; Edge에 password 타입이 없어 입력 중 보임을 설명에 명시), `macAddress`, `wolBroadcast`(255.255.255.255),
`pollInterval` enum(10s/30s/1m/5m, 기본 30s; 푸시 구독 성공 시 5m으로 자동 완화하지 않고 사용자 값 유지),
`offAction` enum(shutdown/suspend/hibernate/lock/turnscreenoff/restart/forceshutdown),
`createDisplayDevice` bool, `language` enum(auto/ko/en).

SSDP로 추가된 장치는 `ipAddress`/`port`가 채워진 상태로 생성되고, 사용자는 시크릿과 MAC만 넣는다.
드라이버는 자기 환경설정을 쓸 수 없으므로, `macAddress`가 비어 있으면 폴링이 저장한 WoL 가능 어댑터 MAC(장치 필드)을 사용한다.

## 6. 드라이버 동작

### 6.1 폴링과 health

- `pollInterval`마다 `GET /st/v1/status`. 성공 → `connection=ok`, health online, 속성 갱신.
- 실패 분류: `401` → `unauthorized`; `403` → 같은 `unauthorized`지만 메시지는
  "허브가 허용 목록에 없습니다"(고칠 곳이 다르다); 연결 거부/타임아웃 → `unreachable`;
  `protocol` 불일치 → `incompatible`(메시지에 "서비스 vX 이상 필요" 또는 "드라이버 업데이트
  필요"). `429`(§8)는 서비스가 멀쩡한데 너무 자주 물은 것뿐이므로 **아무 속성도 바꾸지 않고**
  로그만 남긴다(명령 직후의 즉시 폴링이 같은 초에 걸릴 수 있다).
- `pcStatus.message`는 한 줄이므로 동시에 해당하는 안내가 여러 개면 우선순위로 하나만 고른다:
  오류 > `incompatible` > WoL 미준비 > 업데이트 있음 > 시크릿 없음 > 명령 결과 확인 문구.
- `unreachable`이 2회 연속이면 `powerState`를 `off`로 두되, 직전 푸시가
  `power.stopping(reason=suspend|hibernate)`였다면 `sleeping`/`hibernated`를 유지한다.

### 6.2 전원 상태 머신

```
on --(power.stopping reason=suspend)--> sleeping
on --(power.stopping reason=hibernate)--> hibernated
on --(power.stopping reason=shutdown|restart)--> shuttingDown --(unreachable ×2)--> off
on --(schedule.active for shutdown/restart)--> on (pcSchedule 카드가 표시; powerState는 유지)
off|sleeping|hibernated --(switch on)--> waking --(status ok)--> on
waking --(90s 초과)--> 이전 상태 + message "깨우기 실패: WoL 응답 없음"
any --(status ok)--> on
```

`switch` 속성은 `powerState ∈ {on, waking, shuttingDown}`이면 on, 그 외 off.
PC에서 유예를 취소하면 `schedule.cancelled` 푸시 → 스위치 on 복귀(기존 드라이버의 불일치 문제 해결).

### 6.3 WoL

매직 패킷을 즉시, 2초, 5초 후 3회, 포트 7과 9 모두. `wolReady=false`면 시도는 하되
`message`에 "PC의 어댑터에 WoL이 꺼져 있습니다 · 네트워크 탭 확인"을 표시한다.

### 6.4 푸시 리스너 (P2)

- 드라이버 시작 시 cosock TCP 서버를 임의 포트로 열고 `POST /pc/evt`를 받는다.
  본문의 `machine_id`로 장치를 찾고 `status`를 §6.1과 같은 경로로 반영한다.
- 각 장치가 `connection=ok`가 되면 `subscribe`하고 TTL 80%에 갱신. 실패 시 폴링만으로 동작.
- 허브 IP는 `driver:get_ip()`(hub-local) 사용.

### 6.5 i18n

Edge 환경설정/프레젠테이션은 영어가 기본이다. 사용자에게 보이는 **문자열 속성**
(`lastCommand`, `message`, `origin`)은 `language` 환경설정(auto=허브 로케일 추정 불가하므로 en)
에 따라 `i18n.lua`에서 ko/en 선택. 프레젠테이션 라벨은 영어 + 괄호 한국어 병기는 하지 않는다.

## 7. GUI (Fyne) 변경

네트워크 탭에 **SmartThings** 섹션 추가:

- 연결 상태: "허브 192.168.1.20 · 드라이버 v1.0.0 · 마지막 확인 3초 전" / "연결된 허브 없음".
- 토글 `자동 검색(SSDP) 허용`, `세션 정보 노출(잠금·유휴)`, `사용자 이름 포함`.
- 허브 허용 목록: 현재 허브를 [허용 목록에 추가] 버튼, 목록 표시와 삭제.
- 시크릿이 비어 있으면 "SmartThings 연동에는 시크릿 설정을 권장" 안내와 설정 탭 링크.
- 저장은 기존 saveBar 흐름(dirty 표시·탭 전환 확인)을 그대로 사용.

WebUI(settings.html)는 필드만 노출하고 꾸미지 않는다.

## 8. 보안

- 시크릿은 헤더로만(§4.1). 로그에 남기지 않음(레거시 경로처럼 `***` 마스킹).
- 허브 허용 목록(옵션), 콜백 = 요청 출처 + 사설 IP 검증, 콜백 본문에 시크릿을 넣지 않음.
- `/st/v1/*`는 초당 10회 이상이면 `429`(단순 토큰 버킷, 출처 IP별).
- `description`은 무인증이지만 버전·호스트명만 노출. `discovery=false`면 SSDP 응답도 끈다.

## 9. 테스트

- Go: `st_api_test.go`(인증·허용 목록·명령 모드·예약·취소·status 스키마 골든),
  `st_push_test.go`(구독 검증·TTL·실패 제거·stopping 동기 전송), `st_ssdp_test.go`(M-SEARCH 응답).
- Lua: `tests/run.lua` + 목으로 `state.lua`(status→이벤트 매핑, 상태 머신 전이),
  `wol.lua`(패킷 바이트, 재시도 스케줄), `client.lua`(오류 분류), `push.lua`(본문 파싱, 갱신 타이밍).
  실행: `cd edge && npm test` (CI) / 로컬은 bun으로 `tools/lua.js tests/run.lua`.
- 실기 검증: 사용자의 허브에 채널 등록 후 스위치·명령·예약·취소·푸시·SSDP·디스플레이 확인.

## 10. CI / 배포

- `edge.yml`: push/PR에서 Lua 테스트. 태그 `edge-vX.Y.Z`에서 `@smartthings/cli`로
  `edge:drivers:package edge/` → `edge:channels:assign`(secret `SMARTTHINGS_TOKEN`, `ST_CHANNEL_ID`).
  드라이버 버전은 `edge/config.yml`의 `version`과 태그를 일치시키는 검증 단계 포함.
- 서비스는 기존 `v1.1.0` 릴리즈 절차. README/Wiki에 "SmartThings Edge 드라이버" 페이지와
  채널 초대 링크(사용자가 채널 생성 후 기입).

## 11. 사용자 준비 사항 (에이전트가 할 수 없는 것)

1. SmartThings CLI 설치 후 `smartthings login`. 커스텀 capability 생성:
   `smartthings capabilities:create -i edge/capabilities/<name>.json` ×5 →
   발급된 네임스페이스를 `edge/src/caps.lua`의 `NAMESPACE`와 프로필 파일에 반영(스크립트 `edge/tools/apply-namespace.js` 제공).
   프레젠테이션 `smartthings capabilities:presentation:create`.
2. 채널 생성(`edge:channels:create`), 허브 등록(`edge:channels:enroll`), 채널 ID·PAT를 GitHub Secrets에.
3. 실기 테스트.

## 12. 이슈 매핑

마일스톤 `v1.1.0`, 작업 브랜치 `milestone/v1.1.0`(develop에서 분기). 이슈 브랜치
`feature/<번호>-<slug>`는 `milestone/v1.1.0`으로 머지하고, 마일스톤 완료 시
develop → main → `v1.1.0` 태그.

| 이슈 | 영역 | 내용 | 의존 |
|---|---|---|---|
| #67 | service | `/st/v1` 상태·명령·예약·취소, 헤더 인증, 허용 목록, origin `smartthings`, `turnscreenon`(+Telegram `/screenon`), `last_shutdown_clean`, hubLastSeen, 레이트 리밋 | — |
| #68 | service | 푸시 구독 + notify SmartThings Sink, `power.stopping` reason, `display.changed`, 세션 잠금/유휴 옵트인 이벤트 | #67 |
| #69 | service | SSDP 응답기 + `description`, config `smartthings.*` 핫 리로드 | #67 |
| #70 | gui | 네트워크 탭 SmartThings 섹션, i18n, WebUI 필드 | #67, #69 |
| #71 | edge | 드라이버 골격: config/profile/prefs, 수동 추가, 스위치·WoL·ping·health, fengari 테스트 하네스, 상태 머신 | — |
| #72 | edge | 커스텀 capability JSON/프레젠테이션, `/st/v1` 클라이언트, 상태·명령·예약·취소 매핑, 오류·호환 표시, i18n | #71 |
| #73 | edge | 푸시 리스너·구독 갱신, SSDP 검색, 디스플레이 자식 장치, WoL 재시도/waking | #72, #68, #69 |
| #74 | ci/docs | `edge.yml`(테스트·패키징), `edge/README.md`, 네임스페이스 적용 스크립트, Wiki 페이지·README 갱신, CHANGELOG | 전부 |

마일스톤: https://github.com/Protomothis/smartthings-pc-control/milestone/7
