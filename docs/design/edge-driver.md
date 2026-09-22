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
5. **새 기능**: 상세 화면의 화면 끄기/켜기 버튼, 세션 정보(잠금·유휴, 선택 사항).
6. **설치 경험**: SSDP 자동 검색, 한국어·영어, GUI에서 시크릿 생성·연결 확인.
7. **보안**: 시크릿을 URL 경로가 아닌 헤더로, 허브 IP 허용 목록, 콜백 주소 검증.

PCControl 호환 경로(`/{secret}/{command}`)는 그대로 유지한다. 기존 드라이버
사용자는 아무것도 바꾸지 않아도 된다.

## 2. 범위와 단계

| 단계 | 서비스 | 드라이버 | 결과 |
|---|---|---|---|
| **P1 MVP** | `/st/v1/` 프로토콜(상태·명령·예약·취소), origin `smartthings`, `turnscreenon`, 헤더 인증, 허브 허용 목록 | 프로필·환경설정·수동 추가·스위치/WoL/ping·healthCheck, 커스텀 capability(상태·명령·예약), 오류/호환 표시, 테스트 하네스 | PCControl 동등 + 유예/예약/취소/상태 |
| **P2 푸시** | `/st/v1/subscribe`, notify 버스 SmartThings Sink, 전원·세션·디스플레이 이벤트 | 허브 내 HTTP 리스너, 구독 갱신, 즉시 반영 | 폴링 5분으로 완화, 즉시 상태 |
| **P3 완성** | SSDP 응답기, GUI SmartThings 섹션, 세션 정보 옵트인 | SSDP 검색, WoL 재시도/waking, ko/en, CI 패키징, 문서 | 채널 공개 |

모두 v1.1.0 마일스톤에 포함한다. 단계는 병렬 작업의 의존 순서일 뿐이다.

## 3. 저장소 구성

같은 레포의 `edge/` 폴더에 드라이버를 둔다. 프로토콜 스키마를 서비스와 함께
버전 관리하고, Actions 한 곳에서 패키징한다.

```
edge/
  config.yml                 # Edge driver 메타 (name, packageKey, permissions: lan)
  profiles/
    pc-v6.yml                # main 컴포넌트 프로필(현행 pc.v6, §14.3)
    pc.yml … pc-v5.yml       # v1~v5: 아직 이전되지 않은 장치가 참조 (#79)
  capabilities/              # 커스텀 capability 정의/프레젠테이션 JSON (CLI로 생성)
    pcPower.json  pcPower.presentation.json
    pcAction.json      pcAction.presentation.json
    pcTimer.json    pcTimer.presentation.json
    pcHealth.json      pcHealth.presentation.json
    pcUser.json     pcUser.presentation.json
  src/
    init.lua                 # 드라이버 진입점, lifecycle
    caps.lua                 # 커스텀 capability id 상수 (NAMESPACE 한 곳)
    client.lua               # /st/v1 HTTP 클라이언트 (cosock http)
    discovery.lua            # SSDP M-SEARCH + 수동 추가
    poll.lua                 # 상태 폴링/health
    push.lua                 # 허브 리스너 + 구독 갱신
    wol.lua                  # 매직 패킷, waking 상태 머신
    state.lua                # status JSON → capability 이벤트 매핑
    i18n.lua                 # ko/en 문자열 (속성 문자열용)
    profiles.lua             # 프로필 이름·버전, 기존 장치 이전 (§14.3)
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
  로 두되 `pcHealth.message`에 "no secret" 경고 문자열을 넣는다(`connection` enum에 `noSecret`은 두지 않는다).
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
- `idle_seconds`는 트레이 앱이 보내는 하트비트에서 온다(#77). 서비스는 세션 0에서
  돌아 사용자 입력 시각을 알 수 없고(`WTSINFOEXW.LastInputTime`은 Windows 10/11
  콘솔 세션에서 로그온 시각에 고정되어 사실상 업타임이다), 사용자 세션에 있는
  트레이 앱만 `GetLastInputInfo`를 부를 수 있다. 트레이 앱은 `expose_session=true`
  일 때 30초마다 `POST /api/session/heartbeat`(WebUI API, 세션 쿠키 인증)로 값을
  올리고, 서비스는 마지막 하트비트가 90초 이내일 때만 그 값을 싣는다. 트레이 앱이
  꺼져 있거나 옵션이 꺼지면 `idle_seconds`는 null이다(`locked`·`user`는 WTS에서
  오므로 영향 없음). 유휴 변화는 푸시 이벤트로 내보내지 않는다.
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
{ "protocol": 1, "machine_id": "9f3c...-machine-guid", "type": "schedule.created",
  "at": "2026-09-17T23:05:00+09:00",
  "data": { "command": "shutdown", "origin": "smartthings", "remaining_seconds": 300 },
  "status": { ...GET /st/v1/status 와 동일... } }
```

  매 이벤트에 전체 `status`를 실어 드라이버가 diff 없이 갱신한다. `machine_id`는
  허브가 여러 PC의 이벤트를 status 파싱 전에 구분할 수 있도록 최상위에도 싣는다.
  2초 타임아웃, 실패 1회 재시도, 연속 3회 실패 시 구독 제거.
- 푸시 대상 이벤트: `power.stopping`(data.reason: shutdown/restart/suspend/hibernate/unknown),
  `power.started`, `power.resumed`, `schedule.*`, `remote.*`, `system.updated`,
  `system.update_available`, `display.changed`, `session.locked`/`session.unlocked`(옵트인).
- 구현은 `service/notify` 버스의 **raw tap**(`Bus.Tap`, `service/st_push.go`)이다.
  Sink는 파이프라인 끝에 있어 카테고리 필터·조용한 시간대를 지나오므로, 그것들을
  적용하지 않으려면(장치 상태는 알림이 아니다) 필터 이전 지점이 필요하다.
  `display.changed`·`session.*`는 알림 카탈로그에 없는 순수 장치 상태라
  `Bus.TapOnly`로 tap에만 보낸다(텔레그램에 도달하지 않는다).
  `power.stopping`은 종료 직전이므로 동기 전송(최대 1.5초 대기) 후 계속 종료한다.
  서비스는 절전 시 중지되지 않으므로 `reason=suspend|hibernate`는 SCM 중지가 아니라
  `PBT_APMSUSPEND` 브로드캐스트에서 낸다(§6.2). 어느 경로든 Windows는 이유를 알려
  주지 않아, 직전에 실행한 명령을 힌트로 쓴다.
- 세션 잠금/해제는 세션 0에 이벤트가 오지 않아 5초 폴링으로 감지한다
  (`smartthings.expose_session`이 켜져 있을 때만).

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

### 5.1 프로필 `pc-v6.yml` (main, 이름 `pc.v6`; 버전 규칙은 §14.3)

| capability | 용도 |
|---|---|
| `switch` | on → WoL 시퀀스, off → 환경설정의 기본 off 명령(`mode=default`) |
| `healthCheck` | 폴링/푸시 기반 online/offline |
| `{NS}.pcPower` | `powerState` enum: `on` `sleeping` `hibernated` `off` `waking` `shuttingDown` `unknown` |
| `{NS}.pcAction` | (#82로 `pcControl`에서 이름·id를 바꿨다 — §14.4) 명령 `execute(command, mode?, minutes?)`(상세 화면의 명령 목록과 자동화가 모두 쓴다. `command` enum은 §4.3의 8개에 `wake`를 더한 9개이고 `wake`는 서비스로 나가지 않는 WoL 시퀀스다. `forceshutdown`은 자동화에만 둔다) + 인자 없는 명령 `wake` `suspend` `hibernate` `restart` `shutdown` `lock` `screenOff` `screenOn`(#78의 버튼. 화면에서는 빠졌지만 정의와 핸들러에 남아 옛 프로필의 장치와 씬이 쓴다); attrs `lastAction` enum `none` `wake` `suspend` `hibernate` `restart` `shutdown` `lock` `screenOff` `screenOn`(마지막으로 실행한 명령 — 상세 화면 목록이 보여 주는 값), `lastCommand` string("종료 · SmartThings · 23:05") |
| `{NS}.pcTimer` | attrs `summary` string("종료 · 4분 남음 · SmartThings", 예약 없으면 `""`), `active` bool, `command` string, `remainingSeconds` integer, `executeAt` string(로컬 `HH:MM`), `origin` string; command `cancel()` ; command `schedule(minutes, command?)` — `minutes`는 capability 상으로는 integer 1..1440(서비스 상한과 동일)이고, 프리셋 5/15/30/60/120은 프레젠테이션의 선택지로만 제공한다 |
| `{NS}.pcHealth` | attrs `summary` string("연결됨 · v1.1.0" / "연결 안 됨 · 시크릿 불일치" — #82로 전원 낱말이 빠졌다. 바로 위 줄이 `pcPower`다), `connection` enum(`ok` `unauthorized` `unreachable` `incompatible`), `serviceVersion` string, `updateAvailable` bool, `wolReady` bool, `lastSeen` string(마지막 성공 폴링의 로컬 `HH:MM:SS`), `message` string(사람이 읽는 오류/안내 **한 줄**) |
| `{NS}.pcUser` | attrs `exposed` bool, `summary` string("잠김 · 유휴 20분 · kim"), `locked` bool, `idleMinutes` integer, `user` string — `exposed`는 항상 내보내고(상세 화면의 `visibleCondition` 기준), 나머지는 `session.exposed=false`면 내보내지 않아 마지막 값이 유지된다 |
| `refresh` | 즉시 폴링 |

`summary` 세 개는 #78에서 추가했다. 상세 화면이 원시 속성을 한 줄씩 늘어놓아 읽기
어려웠기 때문에, 드라이버가 문장으로 합쳐 한 줄만 보여 주고 원시 속성은 자동화 조건
전용으로 남긴다. 합치는 문구는 `i18n.lua`의 ko/en을 따른다(§6.5).

#82로 `pcHealth.summary`는 짧아졌다. 전원 낱말을 빼고(바로 위가 `pcPower` 줄이다),
안내는 `message`의 긴 문장 대신 짧은 형태(`i18n.lua`의 `*_short`: "시크릿 미설정 ·
설정 권장", "어댑터 WoL 꺼짐", "업데이트 v1.2.0 사용 가능")를 붙인다. 요약 줄 넷이
한 화면에 모이므로 휴대폰이 긴 줄을 잘라 버리기 때문이다. `message`는 그대로 긴
문장을 유지한다 — 자동화와 이력이 읽는 쪽이다.

### 5.2 디스플레이 자식 장치

제거됨(#81): 본체의 화면 끄기/켜기 버튼으로 대체.

### 5.3 프레젠테이션 (#78 리모컨 모델, #82 최종 배치)

2026-09-22 실기 피드백으로 배치를 확정했다. `pushButton` 줄은 값이 없어 휴대폰이
라벨 옆에 "-"를 그리고(§14.5), 조건부 줄은 `visibleCondition`이 무시돼 늘 보인다.
그래서 **모든 줄이 값을 가지는 드롭다운 중심의 6줄**로 정리했다.

- 대시보드: `switch` + `pcPower.powerState` 상태 문구. (변경 없음)
- 상세: 위에서부터
  1. `pcPower.powerState` 상태 줄 (그 위에 표준 `switch` 토글)
  2. `pcAction`: `list` — `command: {name: "execute", alternatives: 8}`,
     `state: {value: "lastAction.value", alternatives: 9}`. 고르면 곧바로 실행되고,
     줄에는 마지막으로 실행한 명령이 남는다. 순서는 깨우기 · 절전 · 최대 절전 ·
     재시작 · 종료 · 잠금 · 화면 끄기 · 화면 켜기. **강제 종료는 화면에 없다**
     (되돌릴 수 없는 명령은 자동화에서만). alternatives의 `key`는 `execute`의
     `command` enum 값(서비스 명령 이름)이고, `state`의 `key`는 `lastAction` enum
     값이다 — 드라이버가 둘을 `state.action_for`로 잇는다.
  3. `pcTimer`: `list` — `command: {name: "schedule", alternatives: 5·15·30·60·120분
     + `0`(취소)}`, `state: {value: "active.value", alternatives: 예약됨/예약 없음}`.
     `minutes = 0`은 드라이버가 `cancel`과 같은 경로로 보낸다(정의는 그대로다).
  4. `pcTimer.summary` 상태 줄 (예약 없음 / "종료 · 4분 남음 · SmartThings")
  5. `pcHealth.summary` 상태 줄 (전원 낱말 없이, 안내는 짧은 형태)
  6. `pcUser.summary` 상태 줄 (꺼져 있으면 그렇다고 적는다)
- **detailView에 `pushButton`은 하나도 없다**(§14.5). 인자 없는 명령 8개와
  `pcTimer.cancel`은 정의에 그대로 남아 핸들러도 유지된다 — 아직 옛 프로필에 있는
  장치가 그 화면을 쓰고, 씬·허브 로컬 자동화가 부를 수 있는 형태이기 때문이다.
- 원시 속성(`remainingSeconds` `executeAt` `origin` `serviceVersion`
  `updateAvailable` `wolReady` `lastSeen` `idleMinutes` `locked` `user`
  `lastCommand`)은 detailView에서 빠졌지만 정의와 자동화 조건에는 그대로 남는다.
- 자동화: 조건 `powerState`, `pcAction.lastAction`, `pcTimer.active`, `connection`,
  `locked`; 액션 `pcAction.execute`, `pcTimer.schedule`, `switch`.
  `pushButton`은 `automation.actions`에 넣을 수 없다(§14). 자동화에서 예약을
  취소하려면 `schedule(minutes: 0)`을 쓴다.
- 목록이 보내는 `execute(command)`에는 `mode`가 없다. 이때 모드는 환경설정
  `buttonMode`(§5.4)가 정한다(버튼이 하던 것과 같다). 자동화의 `execute`는 자기
  `mode`를 명시하므로 그대로 쓴다.
- 라벨은 프레젠테이션에 **영어**로 두고, capability translations
  (`capabilities/translations/<이름>.{ko,en}.json`)가 휴대폰 로케일에 맞춰 덮어쓴다.
  `smartthings capabilities:translations:upsert <id> --capability-version 1 -i <file>`.
  환경설정(preferences)은 로케일별 변형이 없으므로 프로필에 한국어 우선으로 병기한다.

### 5.4 환경설정(preferences)

`ipAddress`, `port`(5001), `secret`(string; Edge에 password 타입이 없어 입력 중 보임을 설명에 명시), `macAddress`, `wolBroadcast`(255.255.255.255),
`pollInterval` enum(10s/30s/1m/5m, 기본 30s; 푸시 구독 성공 시 5m으로 자동 완화하지 않고 사용자 값 유지),
`offAction` enum(shutdown/suspend/hibernate/lock/turnscreenoff/restart/forceshutdown),
`buttonMode` enum(`default` 설정된 유예 따름 / `immediate` 즉시, 기본 `default`) — 상세 화면 리모컨 버튼의 §4.3 `mode`(#78),
`language` enum(auto/ko/en).

제목과 설명은 **한국어 우선, 영어 괄호 병기**("PC IP 주소 (IP address)")다(#78).
Edge 프로필의 preferences에는 로케일별 변형이 없어 한 벌만 쓸 수 있고, 이 프로젝트는
한국어 우선이다. `title`의 길이 제한(36자)에 걸리지 않도록 영어는 용어만 적는다.

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
- `pcHealth.message`는 한 줄이므로 동시에 해당하는 안내가 여러 개면 우선순위로 하나만 고른다:
  오류 > `incompatible` > WoL 미준비 > 업데이트 있음 > 시크릿 없음 > 명령 결과 확인 문구.
- `unreachable`이 2회 연속이면 `powerState`를 `off`로 두되, 직전 푸시가
  `power.stopping(reason=suspend|hibernate)`였다면 `sleeping`/`hibernated`를 유지한다.

### 6.2 전원 상태 머신

```
on --(power.stopping reason=suspend)--> sleeping
on --(power.stopping reason=hibernate)--> hibernated
on --(power.stopping reason=shutdown|restart)--> shuttingDown --(unreachable ×2)--> off
on --(schedule.active for shutdown/restart)--> on (pcTimer 카드가 표시; powerState는 유지)
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

한국어는 두 층으로 나뉜다(#78).

1. **앱 UI 라벨** — capability 라벨·속성 라벨·enum 값·명령과 인자 라벨. 프레젠테이션에는
   영어로 적고, capability translations가 휴대폰 로케일에 맞춰 덮어쓴다(§5.3). 드라이버는
   여기에 관여하지 않는다.
2. **문자열 속성 값** — `pcHealth.summary`/`message`, `pcTimer.summary`,
   `pcUser.summary`, `pcAction.lastCommand`, `pcTimer.origin`/`command`. 이것들은
   드라이버가 만들어 내므로 `language` 환경설정(auto=허브 로케일 추정 불가하므로 ko,
   프로젝트가 한국어 우선)에 따라 `i18n.lua`에서 ko/en을 고른다.

환경설정(preferences)은 어느 쪽도 아니다 — 로케일별 변형이 없어 프로필에 한국어 우선으로
병기한다(§5.4).

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

1. SmartThings CLI 설치 후 첫 명령(예: `smartthings locations`)에서 브라우저 로그인(2.x에는 `login` 명령이 없다). 커스텀 capability 생성:
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
| #81 | edge | 디스플레이 자식 장치 제거(#78의 화면 끄기/켜기 버튼으로 대체), 허브에 남은 자식 정리 | #73, #78 |

마일스톤: https://github.com/Protomothis/smartthings-pc-control/milestone/7

## 13. 다중 PC 시나리오

같은 LAN에 서비스를 설치한 PC가 여러 대 있는 경우를 기본 전제로 한다. 한 허브가 여러 PC를,
여러 허브가 한 PC를 다루는 경우 모두 동작해야 한다.

### 13.1 식별과 중복 방지

- **장치 식별자 = `machine_id`**(HKLM MachineGuid). SSDP `USN`, `description`, `status`, 푸시 본문
  **최상위**에 모두 `machine_id`를 넣는다. 드라이버는 이 값으로 장치를 찾는다.
- SSDP 검색 결과의 DNI(device_network_id)는 `machine_id`. 이미 같은 `machine_id`를 가진 장치가 있으면
  새로 만들지 않고 IP·포트·호스트명만 갱신한다.
- 수동 추가 장치는 DNI `manual-<random>`으로 만들고, 첫 `status` 성공 시 `machine_id`를 장치 필드에 저장한다.
  이후 SSDP가 같은 `machine_id`를 찾으면 그 장치의 IP를 갱신하고 중복 생성하지 않는다(DNI는 바꾸지 않는다).
- 장치 라벨은 "`hostname` 컴퓨터"(en: "`hostname` PC"). 기존 장치 이름 관행("혁 컴퓨터")에 맞춘 것이며, 사용자가 라벨을 바꾸면 덮어쓰지 않는다.
- 이미지 복제로 MachineGuid가 같은 PC가 둘이면 SSDP에서 하나로 합쳐진다. `description`에 `hostname`을 함께 실어
  드라이버가 "같은 machine_id, 다른 hostname"을 만나면 `pcHealth.message`로 경고한다. 해결은 사용자가 GUID를 재생성하는 것으로 문서에 적는다.

### 13.2 IP 변화(DHCP)와 다중 NIC

- 환경설정 `ipAddress`가 비어 있으면 드라이버는 SSDP로 알게 된 IP(장치 필드)를 쓴다. 채워져 있으면 고정 IP로 간주한다.
- 환경설정 `followDiscovery`(기본 true): SSDP가 같은 `machine_id`를 다른 IP로 알려 오면 필드 IP를 갱신하고 즉시 폴링한다.
  `unreachable`이 되면 드라이버는 다음 폴링 전에 1회 SSDP 단일 검색(ST 지정)으로 IP 재탐색을 시도한다.
- 서비스는 M-SEARCH를 받은 인터페이스의 IP로 `LOCATION`을 만든다(유선+무선 PC에서 허브가 도달 가능한 주소가 나온다).
- 포트가 PC마다 달라도 `LOCATION`과 `description.port`로 전달되므로 문제없다.

### 13.3 폴링·푸시

- 장치가 N개면 폴링 시각을 `pollInterval / N` 간격으로 분산해 동시 요청을 피한다.
- 허브 리스너는 드라이버당 하나. 푸시 본문 최상위 `machine_id`로 장치를 찾고, 모르는 `machine_id`는 로그만 남긴다.
- 구독은 장치(PC)별로 하나. 서비스는 콜백 URL별 구독을 여러 개 가질 수 있으므로 허브가 둘이어도 각각 받는다.
- 시크릿·MAC·브로드캐스트 주소는 장치별 환경설정이다. 서브넷이 다른 PC는 그 서브넷의 브로드캐스트를 넣는다.

### 13.4 텔레그램과 다중 PC (#75, 방침 2026-09-17)

- PC 서비스의 텔레그램은 **"봇 하나 = PC 하나"** 스탠드얼론 기능으로 유지한다. 여러 PC를 한 봇으로 묶는 로직은
  PC 서비스에 넣지 않는다. 여러 PC를 한 봇으로 다루는 것은 별도 프로젝트 **허브 에이전트**(Docker, `/st/v1` 클라이언트)가
  맡는다 — `docs/design/hub-agent.md`.
- Edge 드라이버는 텔레그램과 무관하다(origin 라벨 표시 외 접점 없음).
- PC 서비스에 남기는 것: 알림·`/status` 머리말에 **PC 이름**(기본 hostname, `telegram.pc_name`) 표시(PC가 한 대여도 무해),
  같은 봇 토큰을 여러 PC가 공유해 getUpdates 409 Conflict가 나면 로그·GUI 경고("다른 PC가 같은 봇으로 명령을 수신 중.
  PC마다 봇을 분리하거나 허브 에이전트를 사용")와 폴링 백오프 확대.
- 문서 권고: PC마다 봇 분리. 여러 PC를 한 봇으로 쓰려면 허브 에이전트.

### 13.5 이슈 반영

- #69: `description`·SSDP에 `machine_id`·`hostname`, 인터페이스별 `LOCATION`.
- #68: 푸시 본문 최상위 `machine_id`.
- #73: DNI/중복 방지/IP 추적/`followDiscovery`/폴링 분산/자식 장치 라벨.
- #75(service+gui): 텔레그램 PC 이름 머리말, 409 감지 경고(허브 에이전트 안내 문구).
- #76(service): SSDP용 UDP 1900 방화벽 규칙.
- #74: Wiki "여러 PC 설정" 절.

## 14. capability 생성 결과와 프레젠테이션 규칙 (2026-09-22 실측)

- 계정에 발급된 네임스페이스: **`numbersystem53811`**(개인). 조직 네임스페이스 `towerdegree51000`은 쓰이지 않았다.
- SmartThings는 capability id의 이름 부분을 **소문자**로 바꾼다: `numbersystem53811.pcpower` 등. 정의의 `name`은 camelCase 그대로다.
  `caps.lua`·프로필·JSON의 id는 소문자, 파일 이름은 camelCase를 유지하고 테스트는 대소문자 무시로 매칭한다.
- CLI 2.x에는 `login` 명령이 없다. 인증이 필요한 첫 명령에서 브라우저가 열린다.
- 프레젠테이션 API가 거부한 것과 통과한 형식:
  - `detailView`에 `multiArgCommand` 불가 → `list` 사용. detailView의 `list`는 `{"command": {"name": "...", "alternatives": [...]}}` 객체 형식이어야 하며 `state`를 넣으면 `state.alternatives`도 필수.
  - `automation.actions`에 `multiArgCommand`는 **허용**(문서에는 없음). 각 인자의 위젯(`list`/`numberField`)에 `name`(인자 이름) 필수.
  - `automation.actions`에 `pushButton` 불가 → 예약 취소는 detailView pushButton만 제공.
  - 프레젠테이션 본문의 `id`는 경로의 capability id와 같아야 한다.
- 정의 변경: `pcTimer.schedule(minutes, command?)` — 인자 순서를 바꾸고 `command`를 선택으로 만들어 한 인자만 보내는 detailView `list`가 동작하도록 했다(드라이버는 비어 있으면 `offAction`→`shutdown`으로 보정). `pcControl.execute`의 `mode`/`minutes`도 선택.
- 다섯 정의와 프레젠테이션 모두 계정에 생성 완료(`status: proposed`). 수정은 `capabilities:update` / `capabilities:presentation:update`로만 가능.

### 14.1 #78에서 추가한 것과 아직 확인되지 않은 가정 (2026-09-22)

확정된 규칙(위)에 더해, #78이 새로 기대는 것들이다. 코디네이터가
`edge/tools/sync-capabilities.sh`를 돌릴 때 실제로 확인된다.

> 이 절의 가정 두 가지(`visibleCondition`, 인자 없는 명령의 `pushButton`)는
> 2026-09-22 실기에서 결론이 났다. API는 둘 다 받아 주지만 휴대폰이 조건을 무시하고
> 버튼 줄에 "-"를 그린다 — **§14.5**가 대신 읽어야 할 절이고, #82가 그 결과다.

- **`detailView`의 `visibleCondition`** — 장치 프레젠테이션에는 있는 필드인데, capability
  프레젠테이션의 detailView 항목에서도 받아 주는지는 실측하지 않았다. 쓰는 형식은
  `{"capability": "<이 capability id>", "version": 1, "component": "main",
  "value": "<attr>.value", "operator": "EQUALS"|"NOT_EQUALS", "operand": <값>}`.
  **거부되면** 해당 항목에서 `visibleCondition` 객체만 지운다(다른 것은 그대로).
  그러면 안내·예약·세션 줄이 늘 보이지만, 해당 없을 때 요약이 빈 문자열이라
  치명적이지 않다. 진짜 조건부 표시가 필요하면 장치 프레젠테이션(#74 계열)으로 옮겨야 한다.
- **인자 없는 명령의 `pushButton`** — `pcControl`의 `wake`/`suspend`/… 8개는
  `{"command": "<name>", "argument": null}` 형식으로 detailView에 넣었다. 예약 취소
  버튼이 이미 같은 형식으로 통과했으므로 문제없을 것으로 본다.
- **`capabilities:translations:upsert` 본문 형식** —
  `{"tag": "ko", "label": ..., "attributes": {"<attr>": {"label": ..., "i18n": {"value":
  {"<enum>": {"label": ...}}}}}, "commands": {"<cmd>": {"label": ..., "arguments":
  {"<arg>": {"label": ..., "i18n": {"value": {...}}}}}}}`. enum이 아닌 속성과 인자는
  `label`만 넣는다.
- **preferences `title` 길이** — 한국어 병기 제목이 36자 제한에 걸리는지는
  `edge:drivers:package`에서만 드러난다. 걸리면 괄호 안 영어를 줄인다.
- 프레젠테이션의 `id`가 경로의 capability id와 같아야 한다는 규칙은 새 파일에도 그대로
  적용된다(파일 이름은 camelCase, id는 소문자).

### 14.2 번역 API 실측 (2026-09-22)

- `capabilities:translations:upsert` 본문: `{tag, label, attributes{<attr>{label, i18n{value{<enum>{label}}}}}, commands{<cmd>{label, arguments{<arg>{label}}}}}`.
- **명령 인자의 enum 값 번역은 불가.** `arguments.<arg>.i18n.value{…}`, `arguments.<arg>.i18n{…}`, 배열 형식 모두 422. 서버가 키를 다른 인자의 enum과 대조해 거부한다(인자 하나만 넣어도 동일). 인자 **라벨**만 번역하고, Routine 선택기의 인자 값(shutdown 등)은 영어로 남는다.
- 프레젠테이션 detailView 항목의 `visibleCondition` `{capability, version, component, value:"<attr>.value", operator: EQUALS|NOT_EQUALS, operand}`는 수용됨.
- 정의 갱신 직후 번역 upsert가 "속성 없음"으로 거부될 수 있다(전파 지연). 같은 요청을 몇 초 뒤 다시 보내면 통과한다. `sync-capabilities.sh`는 실패 시 재시도 한 번을 넣을 것(후속).

### 14.3 프로필 버전과 기존 장치 이전 (#79, 2026-09-22 실측)

- 장치의 **화면 정의는 생성 시점의 capability 프레젠테이션으로 굳는다.** 프레젠테이션을
  갱신하고 같은 이름의 프로필(`pc.v1`)을 다시 패키징하면 preference 추가는 반영되지만
  detailView 는 옛 것을 유지한다. 장치를 **새 이름의 프로필**로 옮기면 다시 생성된다.
- 따라서 프레젠테이션을 바꿀 때마다 프로필 이름의 버전을 올린다: `profiles/pc-vN.yml`
  (`name: pc.vN`). **옛 프로필 파일은 패키지에 남긴다** — 아직 옮겨지지 않은 장치가
  참조한다.
- 이름은 `src/profiles.lua` 한 곳에만 둔다(`PC`, 지금까지 쓴 모든 이름 `KNOWN`).
  `discovery.PROFILE`이 여기서 읽는다.
- 이전은 `profiles.migration_for(<현재 이름>)` 순수 함수가 정하고
  (현행이거나 모르는 이름이면 nil), `init`/`added`에서 `profiles.ensure`가
  `device:try_update_metadata({ profile = <새 이름> })`을 pcall 로 호출한 뒤
  `migrated <id> to pc.vN`을 남긴다. 장치당 드라이버 구동 1회만 시도한다.
- **장치의 프로필 이름을 읽는 법**: `device.profile`은 테이블이지만(`id`, `components`)
  `name`이 항상 있지는 않다. 있으면 그것을 쓰고, 없으면 생성 시점에
  `device:set_field("profile_name", <이름>, {persist=true})`로 저장해 둔 값을 쓴다.
  둘 다 없으면 #79 이전에 만들어진 장치이므로 `pc.v1`로 본다.
- 모르는 이름(다른 드라이버의 장치, 이 드라이버보다 새 버전)은 건드리지 않는다.
- #81로 `pc-display.vN` 계열은 패키지에서 빠졌다. 옛 드라이버가 만든 자식이 허브에
  남아 있으면 이전 대상이 아니라 삭제 대상이다: `profiles.is_legacy_child(device)`가
  `parent_assigned_child_key` 또는 `pc-display`로 시작하는 프로필 이름으로 판별하고,
  `device_init`이 `try_delete_device`를 불러
  `removing legacy display child <id>`를 남긴다(장치당 드라이버 구동 1회).

### 14.4 허브의 capability 정의 캐시 (2026-09-22 실측)

- 허브는 커스텀 capability 정의를 **id 단위로 허브 전체에 캐시**하고, 같은 id·버전의 정의를 클라우드에서 바꿔도(`capabilities:update`) 다시 받지 않는다.
  드라이버 재설치, 새 드라이버 id 설치, 장치 삭제·재추가, 프로필 이전 모두 무효. 확인된 갱신 경로는 허브 재부팅(전원 재연결)뿐이다.
- 그래서 정의(속성·명령)를 바꿀 때는 **새 id**로 만든다. v1.1.0 최종 이름: `pcPower` `pcAction` `pcTimer` `pcHealth` `pcUser`(id는 소문자). `pcControl`이 `pcAction`이 된 것도 이 규칙 때문이다(#82: `lastAction` 속성 추가). 쓰이지 않게 된 이름은 참조가 사라진 뒤 `capabilities:delete`로 계정에서 삭제한다.
- 프레젠테이션·번역만 바꾸는 경우는 프로필 버전 업(pc.vN)으로 충분하다(§14.3). 정의가 바뀌면 새 capability id + 프로필 버전 업.
- 배포 후 원칙: 정의 변경은 새 id로, 드라이버는 옛 id 참조를 한 버전 동안 유지하지 않고 바로 새 id로 이전한다(장치는 프로필 이전으로 따라온다).

### 14.5 상세 화면 위젯 실측 (2026-09-22, #82)

실기에서 #78의 리모컨 배치를 확인한 결과다. 여기 적힌 것은 모두 허브·휴대폰에서
직접 본 동작이며, 배치를 바꾸기 전에 읽어야 한다.

- **`pushButton` 줄은 값이 없어 라벨 옆에 "-"가 남는다.** 인자 없는 명령 8개가
  그대로 8줄의 "-"가 됐다. 그래서 detailView에는 `pushButton`을 쓰지 않는다. 대신
  **값이 있는 `list`**를 쓴다: `list.command`가 메뉴, `list.state`가 그 줄이 보여 줄
  값이다. 두 목록의 `key`는 서로 달라도 된다(명령 쪽은 명령 인자의 enum 값, 상태
  쪽은 속성의 enum 값).
- `detailView`의 `list`는 `{"command": {"name": ..., "alternatives": [...]}}` 객체
  형식이어야 하고, `state`를 넣으면 **`state.alternatives`가 필수**다(§14).
- 한 줄에 위젯 두 개를 나란히 놓는 배치는 없다. 2칸으로 보이길 기대한 라벨은 잘렸다.
  긴 라벨과 긴 요약 문자열은 잘린다고 보고 짧게 쓴다.
- `visibleCondition`은 capability 프레젠테이션의 detailView에서 **무시된다**
  (API는 받아 주지만 휴대폰이 반영하지 않는다). 그래서 모든 줄은 해당 사항이 없을
  때도 혼자 읽혀야 한다 — "예약 없음", "세션 정보 꺼짐"처럼.
- 값이 한 번도 emit 되지 않은 속성도 "-"로 보인다. 그래서 `lastAction`은 장치
  추가 시점과 첫 폴링에 `none`을 내보낸다(§5.3).
- `automation.actions`에는 여전히 `pushButton`을 넣을 수 없고 `multiArgCommand`는
  허용된다(§14). 자동화에서 예약을 취소하려면 `schedule(minutes: 0)`을 쓴다.
- 번역은 **명령 인자의 enum 값을 바꾸지 못한다**(§14.2). detailView `list`의
  `alternatives`에 적은 `value`가 그대로 보이므로 영어로 적고, 상태 쪽
  (`attributes.<attr>.i18n.value`)만 로케일을 따른다. 결국 한 줄 안에서 메뉴는
  영어, 값은 한국어가 될 수 있다 — 지금 받아들인 제약이다.

## 15. 정식 릴리스 전 체크리스트 (edge)

1. **프로필 이름 리셋**: 최신 프로필 파일을 `pc.v1`(파일 `profiles/pc.yml`)로 두고, 개발 중 올라간 `pc.v2`~`pc.vN` 파일과 `profiles.lua`의 KNOWN 목록을 `pc.v1`만 남긴다. 사용자 눈에 보이지 않는 이름표일 뿐이라 정식은 v1에서 시작한다. 개발 허브의 "hk 컴퓨터"는 삭제 후 재추가(우리 장치만 해당).
2. **capability 이름 확정**: 최종 이름(`pcPower` `pcAction` `pcTimer` `pcHealth` `pcUser`)이 그대로 v1. 계정에 옛 정의가 남아 있지 않은지 `smartthings capabilities` 목록으로 확인. 배포 후 정의 변경은 새 id로만(§14.4).
3. **버전 표기**: `src/driver_version.lua` = `1.0.0`, 태그 `edge-v1.0.0`(CI가 일치 검증). SmartThings 쪽 드라이버 버전은 패키징 시각 문자열이라 통제 대상이 아니다.
4. **채널 정리**: 개발용 드라이버 버전은 채널에 마지막 것만 남는다. 초대 링크를 README/Wiki 자리표시자에 기입.
5. **서비스**: v1.1.0 정식 태그는 `milestone/v1.1.0 → develop → main → v1.1.0` 순서, 사용자 검토 후.
