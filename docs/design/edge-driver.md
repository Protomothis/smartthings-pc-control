# SmartThings Edge 드라이버 설계

이 프로젝트의 전용 SmartThings Edge 드라이버와, 그 드라이버가 쓰는 서비스 API
`/st/v1`의 계약 문서다. 플랫폼 자체의 제약과 실측 동작은 별도로
[`edge-platform-notes.md`](edge-platform-notes.md)에 있다.

- 드라이버: `edge/` (Lua 5.3)
- 서비스: `service/st_api.go`, `st_push.go`, `st_ssdp.go`, `st_idle.go`, `firewall.go`
- 사용자 안내: `edge/README.md`, Wiki [SmartThings Edge 드라이버]

## 1. 개요와 목표

허브 안에서 로컬로 도는 드라이버가 PC Control 서비스와 직접 이야기해서,
서비스가 이미 알고 있는 것을 SmartThings 앱에 그대로 드러낸다.

1. **실제 전원 상태** — 켜짐·절전·최대 절전·꺼짐·깨우는 중·종료 대기. 스위치는 전원 상태에서 파생되므로 실제와 어긋나지 않는다.
2. **유예와 예약이 보인다** — 남은 시간, 실행 시각, 출처(SmartThings·앱·텔레그램), 취소.
3. **IP를 손으로 넣지 않는다** — SSDP 자동 검색으로 주소·포트·호스트 이름이 채워진 채 장치가 생기고, DHCP로 주소가 바뀌면 따라간다.
4. **조용한 실패가 없다** — 시크릿 불일치·연결 불가·버전 비호환·WoL 미준비를 한 줄로 말한다.
5. **여러 PC** — Windows MachineGuid로 장치를 구분하므로 허브 하나가 여러 PC를, 허브 여럿이 한 PC를 다룰 수 있다.
6. **기존 경로 불변** — 레거시 `/{secret}/{command}`는 그대로다. [PCControl 드라이버](https://github.com/toddaustin07/PCControl) 사용자가 옮겨 갈 의무는 없다.

비목표: 클라우드 연동(SmartApp), 화면 전용 자식 장치, 텔레그램 관련 기능.

## 2. 아키텍처

```
  SmartThings 앱
        │ (클라우드)
  ┌─────┴──────────────────────────────┐
  │ SmartThings 허브                    │
  │  ┌──────────────────────────────┐  │        ┌────────────────────────┐
  │  │ smartthings-pc-control 드라이버│  │        │ Windows PC             │
  │  │  init   lifecycle·명령 핸들러  │  │        │  smartthings-pc-control│
  │  │  poll   주기 상태 조회         │──┼── HTTP ─┼─→ :5001 /st/v1        │
  │  │  push   TCP 리스너 /pc/evt    │←─┼── HTTP ─┼── 이벤트 푸시          │
  │  │  discovery  SSDP M-SEARCH     │──┼── UDP ──┼─→ :1900 SSDP 응답기    │
  │  │  wol    매직 패킷              │──┼── UDP ──┼─→ :7 :9               │
  │  │  state  순수 상태 머신·매핑     │  │        │                        │
  │  └──────────────────────────────┘  │        └────────────────────────┘
  └────────────────────────────────────┘
```

드라이버 모듈은 **순수한 부분과 I/O를 분리**한다. `state`·`i18n`·`profiles`와
`client`/`push`/`discovery`의 파싱·판정 함수는 `st.*`·cosock을 건드리지 않아 허브
없이 단위 테스트한다. 장치와 소켓을 만지는 것은 `init`·`poll`과 각 모듈의 얇은
글루뿐이다.

| 파일 | 역할 |
|---|---|
| `src/init.lua` | 진입점. lifecycle(`init`/`added`/`removed`/`infoChanged`/`doConfigure`)과 capability 핸들러 배선 |
| `src/client.lua` | `/st/v1` HTTP 클라이언트, 오류 분류 |
| `src/state.lua` | status JSON → capability 이벤트 매핑, 전원 상태 머신, 요약 문자열 |
| `src/poll.lua` | 폴링 타이머, 장치 필드, `emit` 글루 |
| `src/push.lua` | 허브 TCP 리스너(`/pc/evt`), 구독·갱신 |
| `src/discovery.lua` | SSDP 검색, 식별·중복 방지, 모델명의 PC id |
| `src/wol.lua` | 매직 패킷, 깨우기 시퀀스 |
| `src/profiles.lua` | 프로필 이름과 장치 이전 |
| `src/features.lua` | v1.2.0 기능(media-notify.md): `status.features` 판정, 새 status 블록 → 이벤트, 명령 가드 |
| `src/caps.lua` | 커스텀 capability id |
| `src/i18n.lua` | 속성 **값** 문구의 ko/en |
| `src/driver_version.lua` | 드라이버 버전(단일 출처) |

## 3. 서비스 프로토콜 `/st/v1`

명령 포트(기본 5001)에 레거시 경로와 나란히 붙는다. 프로토콜 버전은 `1`이며 모든
응답 본문의 `protocol` 필드로 확인한다.

### 3.1 인증과 오류

- 시크릿은 **`X-PC-Secret` 헤더**로만 보낸다. URL에는 절대 넣지 않는다.
- `smartthings.allowed_hubs`가 비어 있지 않으면 목록 밖 출처는 `403`.
- 출처 IP별 초당 10회(버스트 10)를 넘으면 `429` + `Retry-After: 1`.
- `GET /st/v1/description`만 인증이 없다(레이트 리밋은 적용된다).
- 오류 본문은 언제나 `{"error": "…"}`이다.

| 코드 | 뜻 | 드라이버의 `connection` |
|---|---|---|
| 401 | 시크릿 불일치 | `unauthorized` |
| 403 | 허브가 허용 목록 밖 | `unauthorized` (메시지는 별도) |
| 400 | 알 수 없는 명령·모드·범위 | `incompatible` |
| 404 | `/st/v1`이 없는 구버전 서비스 | `incompatible` |
| 429 | 레이트 리밋 | (상태 유지, 아무것도 다시 칠하지 않음) |
| 연결 실패 | PC 응답 없음 | `unreachable` |

### 3.2 `GET /st/v1/status`

```json
{
  "protocol": 1,
  "service_version": "v1.1.0",
  "machine_id": "8f1b…",            // HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid
  "hostname": "DESKTOP-ABC",
  "power": "on",                     // 응답했다는 것 자체가 증거
  "uptime_seconds": 43210,
  "last_shutdown_clean": true,
  "secret_set": true,
  "grace": { "enabled": true, "seconds": 300 },
  "schedule": { "active": true, "command": "shutdown", "origin": "smartthings",
                "remaining_seconds": 240, "execute_at": "2026-09-22T23:05:00+09:00" },
  "last_command": { "command": "lock", "origin": "smartthings", "at": "2026-09-22T22:41:07+09:00" },
  "update": { "available": false, "latest": "v1.1.0" },
  "wol": { "ready": true,
           "selected": { "name": "이더넷", "mac": "B4-2E-99-45-B4-F5", "ip": "192.168.1.30",
                         "wol_enabled": true, "wol_capable": true, "source": "auto" },
           "adapters": [ { "name": "이더넷", "mac": "B4-2E-99-45-B4-F5", "ip": "192.168.1.30",
                           "wol_enabled": true, "wol_capable": true, "selected": true },
                         { "name": "Wi-Fi", "mac": "11-22-33-44-55-66", "ip": "192.168.1.31",
                           "wol_enabled": false, "wol_capable": true, "selected": false } ] },
  "display": "on",
  "session": { "exposed": true, "locked": false, "idle_seconds": 1200, "user": "kim" }
}
```

- 예약이 없으면 `"schedule": {"active": false}`, 실행한 명령이 없으면 `"last_command": null`.
- `wol.selected`는 **매직 패킷을 보낼 어댑터를 PC가 골라 준 결과**다(#96). 드라이버는 이 MAC을 쓰고, 없으면(옛 서비스) 예전 규칙으로 폴백한다(#97). MAC이 있는 어댑터가 하나도 없을 때만 `null`이다.
  - `source`는 `manual`(`smartthings.wol_mac`이 이 어댑터를 가리킴) 또는 `auto`.
  - 자동 규칙: **① 허브의 `/st/v1` 요청이 실제로 들어온 인터페이스를 가진 어댑터** (연결의 로컬 주소를 매 요청 기억해 어댑터의 IPv4 목록과 대조) → ② `wol_enabled` → ③ `wol_capable` → ④ MAC이 있는 첫 어댑터. ②~④에서는 가상 어댑터(`vEthernet` `Hyper-V` `VirtualBox` `VMware` `TAP` `Tailscale` `WireGuard` `Loopback` `Bluetooth`)를 실제 어댑터 뒤로 미룬다 — 실제 어댑터가 하나도 없을 때만 고른다. ①은 증거이므로 가상 어댑터에도 그대로 적용된다.
  - `wol_mac`이 어떤 어댑터와도 맞지 않으면 자동으로 되돌아가고 로그에 한 번 남긴다.
- `wol.ready`는 **선택된 어댑터** 기준이다("아무 어댑터나 하나 켜져 있으면 true"가 아니다).
- `adapters[]`의 `ip`는 그 어댑터의 첫 IPv4(없으면 `""`), `selected`는 위에서 고른 어댑터인지 여부다.
- `grace.seconds`는 이 PC에 설정된 유예 길이(기본 300초 = 5분, 최대 30분)다. 드라이버는 이것을 **"곧 실행될 예약 = PC가 떠나는 중"의 상한**으로 쓴다(§6.9).
- `session`은 옵트인이다. `smartthings.expose_session`이 꺼져 있으면 `{"exposed": false}`뿐이고, 켜져 있어도 세션을 읽을 수 없으면 `locked`가 없다. `idle_seconds`는 트레이 앱의 하트비트가 90초 이내일 때만 실린다.

### 3.3 `POST /st/v1/command`

```json
// 요청
{ "command": "shutdown", "mode": "default", "minutes": 0 }
// 응답
{ "accepted": true, "executed": true, "schedule": { "active": false } }
```

- `command` — `shutdown` `forceshutdown` `restart` `hibernate` `suspend` `lock` `turnscreenoff` `turnscreenon` `ping`.
- `mode` — `default`(PC에 설정된 유예를 따름) / `immediate`(유예 없이) / `grace`(유예 강제). `forceshutdown`은 언제나 즉시다. 유예 대상은 `shutdown` `restart` `suspend` `hibernate` 넷뿐이다.
- `minutes` — `0`이면 즉시 처리, `1`~`4320`(3일, #89)이면 **예약**으로 바뀌고 출처는 `smartthings`다. 기존 예약은 대체된다. 같은 상한을 `/api/schedule`과 텔레그램 `/shutdown N`, 앱의 예약 탭이 함께 쓴다.
- 유예로 미뤄진 경우 `executed`는 `false`이고 `schedule`이 채워진다.

### 3.4 `DELETE /st/v1/schedule`

`{"cancelled": true}` — 취소할 예약이 없었으면 `false`.

### 3.5 `POST /st/v1/subscribe`, `DELETE /st/v1/subscribe/{id}`

```json
// 요청
{ "callback": "http://192.168.1.20:41234/pc/evt", "ttl_seconds": 600, "driver_version": "1.0.0" }
// 응답
{ "id": "sub-1", "expires_at": "2026-09-22T23:15:00+09:00" }
```

- 콜백은 **`http://`이고 호스트가 요청 출처 IP와 같아야** 하며 사설·링크로컬·루프백 대역이어야 한다. 아니면 `400`.
- TTL은 60~3600초, 생략하면 600초. 같은 콜백으로 다시 구독하면 갱신이고, 같은 호스트의 다른 콜백이 오면 옛 구독을 대체한다.
- 구독은 메모리에만 있다. 서비스가 재시작하면 드라이버의 다음 폴링이 다시 구독한다.
- 콜백 본문:

```json
{ "protocol": 1, "machine_id": "8f1b…", "type": "schedule.created",
  "at": "2026-09-22T23:01:00+09:00",
  "data": { "command": "shutdown", "origin": "smartthings" },
  "status": { …GET /st/v1/status와 같은 문서… } }
```

- 보내는 `type`: `power.stopping|started|resumed`, `schedule.*`, `remote.*`, `system.updated|update_available`, `display.changed`, `session.locked|unlocked`(세션 노출을 켠 경우만).
- 알림 카테고리 필터와 조용한 시간대는 **적용되지 않는다.** 장치 상태는 알림이 아니다.
- `power.stopping`은 종료가 진행되기 전에 **동기로**(최대 1.5초) 보낸다. `data.reason`이 `suspend`/`hibernate`/`shutdown`/`restart`를 구분해 주므로 타일이 "꺼짐" 대신 "절전"을 보여 준다.
- `reason`을 정하는 순서(#87): ① 최근 2분 안에 이 서비스가 실행한 전원 명령(절전·최대 절전을 아는 유일한 출처) → ② 시스템 종료면 **System 로그의 User32 이벤트 1074**(최근 120초, `wevtutil qe … /f:xml`의 `param5` = Shutdown Type)로 `restart`/`shutdown` 구분 → ③ 시스템 종료면 `shutdown`, 단순 서비스 중지면 `unknown`. SCM은 시스템 종료인지만 알려 줄 뿐 재시작인지 전원 끄기인지는 말해 주지 않는다. ②는 ①이 없을 때만, 1.5초 제한으로 돈다.
- 전송은 2초 타임아웃에 재시도 1회, 연속 3회 실패하면 구독을 지운다. 매 이벤트에 전체 status가 실려 드라이버는 차이를 계산하지 않는다.

### 3.6 SSDP와 `GET /st/v1/description`

- 검색 대상: `urn:smartthings-pc-control:device:pc:1`. `ssdp:all`에도 응답하며 응답의 `ST`는 언제나 구체적인 대상이다.
- `LOCATION`은 **M-SEARCH를 받은 인터페이스의 주소**로 만든다(`http://<ip>:<port>/st/v1/description`). `USN`은 `uuid:<machine_id>::<ST>`.
- `MX`는 3초로 상한을 두고 그 안에서 무작위로 지연시켜 답한다. 출처당 1초에 한 번만 답한다.
- 설명 문서는 무인증이고 장치를 만드는 데 필요한 것만 담는다.

```json
{ "protocol": 1, "machine_id": "8f1b…", "hostname": "DESKTOP-ABC",
  "service_version": "v1.1.0", "port": 5001, "secret_set": true }
```

- 인바운드 **UDP 1900** 방화벽 규칙 *SmartThings PC Control SSDP*는 설치 때 만들고, 서비스가 시작할 때마다 조건 없이 다시 확인한다. 제거는 `uninstall`에서만 한다.
- **응답기는 끌 수 없다(#95).** 장치를 추가할 경로가 검색뿐이므로 서비스가 도는 동안 항상 켜져 있고, 접근 제어는 시크릿과 `allowed_hubs`가 맡는다. 응답한 M-SEARCH의 출처 IP·시각을 메모리에 하나 남기고, 응답기 상태(소켓·방화벽 규칙)와 함께 `GET /api/st/hub`의 `machine_id`·`ssdp: {running, firewall_rule, last_search}`로 내보낸다. 앱은 이것으로 "허브의 검색이 이 PC까지 왔는가"를 보여 준다.

### 3.7 `config.json`의 `smartthings`

전부 핫 리로드다(저장 즉시 반영, 재시작 불필요). SSDP 응답기는 항상 켜져 있으므로
설정이 없다. 예전의 `discovery` 키는 읽어도 무시하고 다음 저장에서 지운다(#95).

| 키 | 기본 | 뜻 |
|---|---|---|
| `allowed_hubs` | `[]` | `/st/v1/*`를 쓸 수 있는 허브 IP. 비어 있으면 모두 허용 |
| `expose_session` | `false` | 잠금 여부·유휴 시간을 status와 푸시에 포함 |
| `expose_session_user` | `false` | 위가 켜져 있을 때 로그인 사용자 이름까지 포함 |
| `wol_mac` | `""` | WoL 매직 패킷을 보낼 어댑터의 MAC. 비어 있으면 서비스가 자동 선택(§3.2 `wol.selected`). `B4-2E-99-45-B4-F5` / `b4:2e:99:45:b4:f5` / `b42e9945b4f5` 모두 받아 대문자 하이픈 형태로 저장하고, 어느 어댑터와도 맞지 않는 값은 자동으로 되돌린다 |

## 4. 장치 모델

한 장치가 한 PC다. 자식 장치는 만들지 않는다. 표준 capability `switch`와 `refresh`에
커스텀 capability 여섯을 더한다(네임스페이스 `numbersystem53811`, id는 소문자).

| capability | 속성 | 명령 |
|---|---|---|
| `pcPower` | `powerState` enum: `on` `sleeping` `hibernated` `off` `waking` `shuttingDown` `unknown` | – |
| `pcRemote` | `lastAction` enum(= `execute`의 `command` enum), `supportedCommands` string 배열, `lastCommand` string("종료 · SmartThings · 23:05") | `execute(command, mode?, minutes?)`, 인자 없는 `wake` `suspend` `hibernate` `restart` `shutdown` `lock` `screenOff` `screenOn` |
| `pcDefer` | `summary` string, `status` enum `idle`\|`scheduled`, `active` bool, `command` string, `remainingSeconds` int(0~259200), `executeAt` string(`HH:MM`), `origin` string, `planCommand` enum `shutdown` `restart` `suspend` `hibernate`, `minutesPick` enum `-1`(한 값) | `schedule(minutes: 문자열 enum `-1` `0` `5`…`4320`, command?)`, `cancel()`, `setPlanCommand(command)` |
| `pcUser` | `exposed` bool, `summary` string, `locked` bool, `idleMinutes` int, `user` string | – |
| `pcInfo` | `summary` string, `connection` enum `ok` `unauthorized` `unreachable` `incompatible`, `serviceVersion`, `updateAvailable` bool, `wolReady` bool, `lastSeen` string, `message` string, `versions` string | – |
| `pcVersion` | `versions` string("v1.1.0 · 드라이버 1.0", 업데이트가 있으면 " · 업데이트 v1.2.0") | – |

- `execute`의 `command` enum은 서비스 명령 여덟에 `wake`와 `none`, 그리고 #93의 `busyOff` `busyRestart` `busyWake` `busySleep` `busyHibernate`를 더한 열다섯이다. `wake`는 서비스로 나가지 않는 WoL 시퀀스이고, **`none`과 `busy*`는 아무것도 하지 않고 폴링만 한다** — 목록을 고르지 않고 닫으면 휴대폰이 그 줄의 현재 값을 인자로 보내기 때문이다.
- `lastAction`은 **쉬는 값**에 머문다. 평소에는 `none`("명령 선택…"), 전원 전환 중에는 `busy*`("종료 진행 중…")다(§6.9). 무엇이 실행됐는지는 `lastCommand`가 말한다.
- `supportedCommands`(#93)는 명령 목록이 보여 줄 키의 배열이다. 평소에는 메뉴 전체(`wake` `suspend` `hibernate` `restart` `shutdown` `lock` `turnscreenoff` `turnscreenon`), 전환 중에는 지금 쉬는 `busy*` 하나뿐이다 — 그 값은 메뉴 항목이 아니므로 고를 것이 없어지기를 노린다. 프레젠테이션의 `supportedValues`가 이 속성을 읽는다. **실기 확인 대기**(플랫폼 노트 "supportedValues"). 빈 배열은 쓰지 않는다.
- `schedule`의 `minutes`는 **문자열 enum**이다: `-1` `0`과 프리셋 열여섯(`5` `10` `15` `30` `45` `60` `90` `120` `180` `240` `360` `480` `720` `1440` `2880` `4320`). **`-1`은 무동작**(폴링만), **`0`은 취소**, 나머지는 예약이다. 드라이버는 `tonumber`로 숫자를 되읽는다. `remainingSeconds`의 상한은 가장 긴 프리셋에 맞춘 259200이다.
- 문자열인 이유(#91, 실측): 목록을 고르지 않고 닫을 때 나가는 현재 값은 프레젠테이션의 `argumentType` 변환을 **거치지 않는다.** `schedule(-1)`은 허브에 닿았지만 `schedule("-1")`은 `422 commands[0].arguments[0]: string found, integer expected`로 클라우드에서 막혔다. 그래서 목록이 보내는 인자는 문자열 enum으로 정의하고, 프레젠테이션에서 `argumentType`은 뺀다. 정의가 바뀌었으므로 capability id도 `pcDelay` → `pcDefer`다 — 허브가 정의를 id로 캐시한다(#89에는 같은 이유로 `pcPlanner` → `pcDelay`였다).
- 클라우드가 인자를 정의로 검증하므로 목록이 보내는 값 — 고른 값이든 닫을 때 나가는 현재 값이든 — 이 모두 정의 안에 있어야 한다.
- `minutesPick`은 `-1` 한 값뿐인 enum이고, 예약 시간 목록이 쉬는 자리다. `lastAction`이 `none`에 머무는 것과 같은 이유다 — 목록을 고르지 않고 닫으면 그 줄의 현재 값이 `minutes` 인자로 나간다. 그 전에는 이 줄이 `status`에 묶여 있어 `schedule(minutes: "idle")`이 나갔고, 클라우드가 거부해 "네트워크 오류" 팝업만 떴다.
- `pcDefer.status`는 `active`의 문자열 판이고, 이제 자동화 조건 전용이다. 목록의 `state`는 bool을 읽지 못한다.
- `pcInfo.versions`는 정의에 남아 있고 계속 emit 되지만, 화면에 그려지는 줄은 `pcVersion.versions`다.

### 4.1 v1.2.0 기능 (드라이버 1.1.0, `docs/design/media-notify.md`)

서비스 v1.2.0의 기능은 되도록 **표준 capability**로 드러낸다. 표준은 정의 캐시 문제가 없고(플랫폼 노트 "허브의 정의 캐시") 앱이 슬라이더·토글·버튼을 스스로 그린다.

| capability | 컴포넌트 | 속성 ← status | 명령 → `/st/v1/command` |
|---|---|---|---|
| `audioTrackData` (표준, #118) | main (또는 `media`, §6.6) | `audioTrackData` = `{title, artist?, album?}` ← `media.title/artist/album` — `features`에 `nowplaying`(옵트인)이 있고 제목이 있을 때만. 블록은 있는데 제목이 없으면 `{title: "재생 중인 미디어 없음"}`(옵트인 꺼짐이면 "재생 정보 꺼짐"), 블록이 없으면(#117 이전) 내보내지 않음. 빈 필드는 빼고 `""`는 보내지 않는다 | – |
| `mediaPlayback` (표준) | main (또는 `media`) | `supportedPlaybackCommands` = `play` `pause` `stop` (상수). #118: `playbackStatus` ← `media.status`(`playing`/`paused`/`stopped`, `none` → `stopped`). 블록이 없으면 보내지 않음(`features.PLAYBACK_RESTING`, 실측 대기) | `play` `pause` `stop`, `setPlaybackStatus(playing\|paused\|stopped)` → `play`/`pause`/`stop` |
| `mediaTrackControl` (표준) | main | `supportedTrackControlCommands` = `nextTrack` `previousTrack` (상수) | `nextTrack` → `next`, `previousTrack` → `prev` |
| `audioVolume` (표준) | main | `volume` ← `audio.volume`(0–100) | `setVolume(v)` → `volume` + `value`, `volumeUp`/`volumeDown` → `volumeup`/`volumedown`(`value` 없음 = 서비스 기본 5) |
| `audioMute` (표준) | main | `mute` ← `audio.muted` (`muted`/`unmuted`) | `mute`/`unmute`, `setMute(state)` |

| `pcPreset` (커스텀 `numbersystem53811.pcpreset`, #113) | main | `lastPreset` enum `none` `1`…`10`(목록이 쉬는 값), `names` string ← `presets[]` ("1 게임 모드 · 2 방송 시작" / "없음" / 옛 서비스면 "서비스 v1.2.0 필요"), `supportedSlots` string 배열 ← 등록된 슬롯(없으면 `["none"]`) | `run(slot: 문자열 enum none\|1…10)` → `preset` + `value` N |
| `pcActivity` (커스텀 `numbersystem53811.pcactivity`, #114) | main | `activity` enum `none` `game` `work` `media` `stream` `other` ← `activity.kind`(모르는 kind는 `other`), `summary` string "게임 중 · Steam" / "없음" / 옵트인 꺼짐·옛 서비스 "꺼짐" | – (루틴 조건 전용) |
| `switch` (표준, #115) | **`awake`** (label "잠들지 않기") | `switch` ← `awake.on` (`on`/`off`; 블록이 없는 옛 서비스는 `off`) | `on` → `awake` + `value` = 환경설정 `awakeMinutes`(기본 60, 0 = 끌 때까지), `off` → `awakeoff` |
| `pcToast` (커스텀 `numbersystem53811.pctoast`, #108) | main | `lastMessage` string(200자) — status가 아니라 드라이버가 가진다: 마지막으로 보낸 문구, 보낸 적이 없으면 "없음"/"None" | `send(text: string, maxLength 200)` → `POST /st/v1/notify {text}` |
| `battery`, `powerSource` (표준, #116) | **`battery`** (label "배터리", `-battery` 프로필에만) | `battery` ← `battery.percent`(-1이면 내보내지 않음), `powerSource` ← `battery.ac` (`mains`/`battery`). `present`가 거짓이면 아무것도 내보내지 않는다 | – |

- 요청 본문은 `{command, value?}`(`client.action`)이다. `mode`·`minutes`는 보내지 않는다 — 이 명령들은 즉시 실행이고 예약되지 않는다.
- **볼륨·음소거는 읽은 값이 있을 때만** 내보낸다(`audio.available` 참, 또는 `updated_at`이 있음). 서비스가 한 번도 재지 않은 0으로 슬라이더를 끌어내리지 않는다.
- **명령 가드**(`features.refusal`): 마지막 status의 `features`에 해당 기능(`audio`·`media`)이 없으면 보내지 않는다. 키 자체가 없으면 옛 서비스 → "서비스 v1.2.0 필요", 키는 있는데 기능이 없으면 "이 PC에서 지원 안 함" — 단 `audio`·`media`는 서비스가 `media.enabled`일 때만 싣으므로(#104/#105) "미디어 제어 꺼짐" — , `audio.available=false`면 "사용자 없음". 서비스의 거절은 `409 no_user_session` → "사용자 없음", `403 media_disabled` → "미디어 제어 꺼짐", `501 unsupported` → "이 PC에서 지원 안 함", `502 failed`·`504 timeout` → "PC에서 실행 실패"(`features.error_note`, 본문의 `error` 코드로 가른다 — `client.classify`는 5xx를 `unreachable`로 보므로 그보다 먼저). 코드 없는 403은 예전대로 허브 허용 목록이다. 409는 `client.classify`에서 `conflict`다(전에는 `unreachable`로 떨어졌다).
- 막힌 명령은 그 줄의 현재 값을 강제로 다시 내보내고(회전 표시 뒤 오류 방지), `pcInfo.message`·`summary`에 이유를 띄운다(§6.9의 `emit_note`와 같은 모양). 성공하면 바로 폴링하면서 그 줄들을 강제로 내보낸다(`poll.once(..., {force = rows})`).
- 이번 구동에서 아직 status를 읽지 못했으면(허브 재시작 직후) 명령 전에 한 번 폴링한다. 그래도 모르면 "PC에 연결할 수 없습니다".
- 전원 전환 가드(§6.9)는 적용하지 않는다. 종료 유예 중의 볼륨 조절은 해가 없고, 깨우는 중에는 요청이 연결 실패로 끝난다.
- **프리셋 목록(#113)**: 목록 항목은 프레젠테이션에 고정이라 "프리셋 1 (Preset 1)"…"프리셋 10"의 슬롯이고, 비어 있는 슬롯은 `supportedValues: "supportedSlots.value"`로 숨긴다(#93과 같은 실험, 실측 대기). 이름은 따로 `names` 상태 줄. 목록이 쉬는 값은 `none`("프리셋 선택…")이고 `run("none")`은 줄에 강제로 답만 한다. 실행에 성공하면 `lastPreset`을 그 슬롯("프리셋 3 실행함")으로 강제로 내보내고, `poll.PRESET_HOLD_SECONDS`(5초)가 지난 첫 폴링·푸시가 `none`으로 되돌린다 — 바뀔 때 강제 한 번 + 다음 호출에 한 번 더, 그 뒤로는 보내지 않는다(`lastAction`의 규칙, 플랫폼 노트 "강제 이벤트 연발"). 그 몇 초 동안 줄이 "3"에 쉬므로 목록을 그냥 닫으면 `run("3")`이 온다. **줄이 보여 주는 바로 그 슬롯의 `run`은 무동작**으로 받는다 — 같은 프리셋을 연달아 두 번 실행하지 않는다. 등록되지 않은 슬롯(루틴)은 보내지 않고 "프리셋 7 비어 있음". 원격은 슬롯 번호만 보낸다(media-notify.md §10).
- **활동(#114)**: 켜져 있다는 판단은 `activity.enabled`와 `features`의 `"activity"` 둘 다다(서비스는 옵트인이 켜져 있을 때만 기능을 싣는다, #110). 요약은 24자(코드 포인트) 안에서 라벨을 뒤에서부터 줄인다 — 전부 → "첫 라벨 외 N"("+N") → 첫 라벨 → 낱말만. 푸시 `activity.changed`도 전체 status를 싣고 오므로 폴링과 같은 `apply_status`로 바로 반영된다.
- **잠들지 않기(#115)**: 표준 `switch`가 두 컴포넌트에 있으므로 핸들러는 `command.component`로 가른다 — `main`(또는 없음)은 전원(§6.2), `awake`는 `awake`/`awakeoff`. 기능 가드는 `"awake"`이고 사용자 세션은 필요 없다. 켜져 있는 동안 다시 켜면 지금부터 새 기간이다(§12). 막힌 명령은 토글을 마지막 status의 값으로 강제로 되돌린다. 전원 전환 가드(§6.9)는 적용하지 않는다 — 전원을 움직이지 않는다. 푸시 `awake.changed`로 바로 반영된다.
- **PC 알림(#108)**: 문구는 제어 문자를 공백으로, 연속 공백을 하나로, 양끝을 다듬고 200자(코드 포인트)에서 "…"로 자른다. 비면 보내지 않는다("보낼 문구 없음"). 입력 줄은 `lastMessage`에 묶여 있고 앱은 그 속성의 이벤트를 기다리므로, **`send`마다 그 줄에 강제로 답한다** — 보냈으면 보낸 문구(같은 문구를 두 번 보내도 `state_change`), 거절·실패면 지금 값을 다시. 보낸 문구는 persist 해 재시작 뒤에도 줄에 남고, 폴링·푸시가 강제 없이 다시 내보내 구동마다 첫 번째만 강제된다(`poll.FIRST_FIELD`). 가드는 `"notify"`, 결과 문구는 `pcInfo.message`에만 — 보냈으면 "PC에 메시지를 보냈습니다", 옛 서비스·기능 없음은 §4.1 위의 문구, `403 notify_disabled` "PC 알림 꺼짐", `409` "사용자 없음", `429` "잠시 후 다시"(서비스의 출처별 분당 10회).
- **왜 커스텀 `pcToast`인가**: 처음에는 표준 `notification`을 썼지만 앱이 그 줄을 "텍스트 표시"로 부르고, 표준 capability의 라벨은 장치 쪽에서 덮어쓸 수 없다(플랫폼 노트 "표준 capability"). 그래서 커스텀 capability로 "PC에 메시지 보내기"를 번역 파일에 둔다(media-notify.md §5). 소리내어 읽기는 없앴다 — 그것까지 있던 `pcMessage`(Dev 채널의 `pc.v3`)와 v2의 표준 두 핸들러도 함께 지웠다. 명령만 있던 `pcNotify`(`pc.v4`)는 입력 줄이 속성에 묶이지 않아 회전 뒤 "네트워크 오류"로 끝났고(2026-10-01), 속성을 더하는 것은 정의 변경이라 새 id `pcToast`가 됐다. 셋 다 공개된 적이 없어 옮겨 줄 장치는 개발 장치뿐이고, 그 장치는 첫 `init`에서 v5로 옮겨진다.
- **배터리(#116)**: 데스크톱에 빈 배터리 카드가 생기지 않도록 배터리는 `-battery` 프로필에만 있다. 폴링·푸시마다 `profiles.apply_battery(device, status.battery.present)`가 장치의 프로필과 status를 비교하고, **연속 두 번**(`profiles.BATTERY_VOTES`) 같은 답이 나와야 같은 스타일의 반대쪽으로 옮긴다(`pc-tv.v5` ⇄ `pc-tv-battery.v5`). 한 번 튀는 값은 무시하고, 거절된 대상은 같은 구동에서 다시 묻지 않는다. 옮기면 1초 뒤 `repaint_soon`(폴링 안에서 요청을 겹치지 않으려고 타이머로). 답은 `profiles.BATTERY_FIELD`에 persist 해 다음 버전 이전(`ensure`)이 바로 맞는 쪽으로 가게 한다. 옮기기 전의 배터리 이벤트는 컴포넌트가 없어 건너뛰고, 옮긴 뒤의 다시 칠하기가 채운다. 푸시 `battery.changed`는 전체 status를 싣고 온다.

## 5. 화면 구성

**대시보드** — 타일의 문구는 `pcPower.powerState`("절전 (Sleeping)", "종료 대기 (Shutting down)" …), 토글은 표준 `switch`다(#101).

- `pcPower` 프레젠테이션의 `dashboard.states`는 `{{powerState.value}}` 하나이고, 대안 문구는 enum 일곱 값 모두에 상세 줄과 **같은** "한국어 (English)" 병기를 쓴다 — 타일과 상세 줄이 다른 말을 하지 않게. `dashboard.actions`는 비워 둔다. 토글은 `switch` capability 자신의 프레젠테이션이 준다.
- 다른 커스텀 capability의 `dashboard.states`는 비어 있다. 타일 자리를 다투지 않는다.
- 이 문구는 #83부터 프레젠테이션에 들어 있었다. #101 시점에 타일이 토글만 보였다면 원인은 프레젠테이션 파일이 아니라 **장치 화면 쪽**이다 — 화면은 장치가 올라탄 프로필 이름으로 굳고(플랫폼 노트 "프로필과 화면 생성"), 자동 생성된 화면이 타일에 무엇을 쓰는지는 아직 재지 않았다. **실측 대기**: Dev 채널에서 새 장치의 타일을 보고, 문구가 없으면 프로필(capability 순서 또는 화면 정의) 쪽을 고친다.

**상세 화면** — 앱이 상태 줄과 조작 줄을 각각 한 카드로 모은다.

| 상태 카드 | 값 |
|---|---|
| 전원 상태 | `pcPower.powerState` |
| 마지막 실행 | `pcRemote.lastCommand` |
| 예약 요약 | `pcDefer.summary` |
| 세션 | `pcUser.summary` |
| 상태 | `pcInfo.summary` |
| 버전 | `pcVersion.versions` |

| 조작 카드 | 위젯 |
|---|---|
| 명령 | `pcRemote.execute` 목록(깨우기·절전·최대 절전·재시작·종료·잠금·화면 끄기/켜기). 줄이 쉬는 값은 `lastAction`의 "명령 선택… (Select a command)", 전원 전환 중에는 "종료 진행 중… (Shutting down…)" 계열이고 목록에 담기는 항목은 `supportedCommands`가 정한다(#93) |
| 예약할 명령 | `pcDefer.setPlanCommand` 목록 |
| 예약 시간 | `pcDefer.schedule` 목록(#89: 5·10·15·30·45분, 1·1.5·2·3·4·6·8·12시간, 1·2·3일, 그리고 취소). 줄이 쉬는 값은 `minutesPick`의 "시간 선택… (Pick a delay)" |

| 미디어 묶음 (#107 #118, 표준) | 곡 정보(`audioTrackData`) → 재생·일시정지·정지(실제 상태 반영) → 이전·다음 곡 → 볼륨 슬라이더 → 음소거 토글. 기본은 main, 대안은 컴포넌트 `media`(§6.6) |
| 프리셋 (#113) | `pcPreset.run` 목록(등록된 슬롯만, `supportedSlots`). 쉬는 값 "프리셋 선택… (Pick a preset)", 실행 직후 잠깐 "프리셋 3 실행함 (Preset 3 started)". 상태 카드에 이름 줄 `pcPreset.names` |
| 활동 (#114) | 상태 줄 `pcActivity.summary` 하나. 루틴 조건 "활동이 게임" |
| PC 알림 (#108, `pcToast`) | 문구 입력 줄 하나: "PC에 메시지 보내기"(`send`). `textField`, 1–200자, 값은 `lastMessage`(마지막으로 보낸 문구, 처음엔 "없음"). 루틴 동작에도 같은 것(명령만) |
| 잠들지 않기 (#115) | 컴포넌트 `awake`의 표준 스위치 토글. 루틴 동작·조건에 그대로 쓴다 |
| 배터리 (#116) | 컴포넌트 `battery`의 표준 `battery`(잔량 %)·`powerSource`(전원 공급원). 배터리가 있는 PC(`-battery` 프로필)에만 |

- 카드 안의 순서는 프로필의 capability 목록 순서를 따른다. 그래서 `pcVersion`이 edge-v1.0 capability의 맨 끝이고, v1.2.0 capability는 그 뒤에 media-notify.md §15 "UI 구성" 순서로 온다(미디어 묶음 → 나머지). 표준 capability의 줄이 우리 상태·조작 카드에 섞이는지, 따로 그려지는지는 실측 대기다(media-notify.md §16).
- 라벨은 번역 파일(ko/en)의 `{{i18n…}}` 템플릿이고, **값 문구는 프레젠테이션의 `alternatives[].value`에 "한국어 (English)"로 병기**한다. 앱이 값 라벨에 번역을 적용하지 않기 때문이다.
- 모든 상태 줄은 해당 사항이 없을 때도 문구를 갖는다("없음 (None)", 예약 "없음", 세션 "꺼짐", 버전의 서비스 자리에 `v?`). 빈 문자열은 화면에서 "-"로 보인다.
- **값 문구는 줄 라벨을 되풀이하지 않는다**(#87). 라벨이 이미 "예약"·"세션"이라고 말하고 있고, 값 칸은 휴대폰이 잘라 낸다. 각 줄이 말하는 것:
  - `pcInfo.summary` — "연결됨" / "연결 안 됨 · 시크릿 불일치·응답 없음·버전 불일치" / 어댑터 WoL이 꺼져 있으면 "연결됨 · WoL 꺼짐". 시크릿 권장·업데이트 안내는 `pcInfo.message`에만 남는다(당장 할 일이 아니라 읽을 거리다).
    - #102 **가동 시간**: 연결됨이면 `status.uptime_seconds`를 붙인다 — "연결됨 · 3일 2시간" / "Connected · 3d 2h". 1분 미만은 붙이지 않고, 1시간 미만은 "N분"("Nm"), 하루 미만은 "N시간 M분"("Nh Mm", 딱 떨어지면 "N시간"), 하루부터는 "N일 M시간"("Nd Mh", 딱 떨어지면 "N일")이다. 예약 요약(#89)과 같은 단위 사다리다.
    - #102 **마지막 확인**: `unreachable`이고 성공한 폴링이 한 번이라도 있었으면 "응답 없음 · 마지막 확인 12분 전" / "No reply · seen 12m ago". 단위는 가장 큰 하나만("N분 전" → "N시간 전" → "N일 전", 1분 미만도 "1분 전"). 한 번도 응답받지 못한 PC는 예전 그대로 "연결 안 됨 · 응답 없음"이고, 시크릿·버전 불일치도 예전 문구다 — 그 PC는 답은 하고 있으므로 "언제 봤나"가 요점이 아니다. 전원 낱말("꺼짐")은 넣지 않는다(#82, 바로 위 전원 상태 줄의 몫). 영어가 "No response" 대신 "No reply"인 것은 24자 때문이다.
    - 시각은 `poll.LAST_SEEN_FIELD`(epoch 초, persist)에 남는다. 성공한 폴링과 모든 푸시가 쓰지만, 저장된 값이 `poll.LAST_SEEN_STEP`(60초)보다 오래됐을 때만 쓴다 — 줄은 분 단위로만 말하고 persist 필드 쓰기는 허브 쓰기다. 실패한 폴링은 읽기만 한다.
    - **24자 예산**(`state.SUMMARY_MAX_CHARS`, 코드 포인트) 안의 우선순위: WoL 꺼짐 경고 > 어댑터 이름 > 가동 시간. 넘치면 가동 시간을 먼저, 그다음 어댑터 이름을 뺀다("연결됨 · WoL 꺼짐 (이더넷) · 5분" → "연결됨 · WoL 꺼짐 (이더넷)" → "연결됨 · WoL 꺼짐"). 마지막 확인 줄이 넘치면 예전 문구로 돌아간다.
  - `pcUser.summary` — "사용 중" / "잠김"(유휴 1분부터 " · 23분") / 노출을 끄면 "꺼짐". 서비스가 사용자 이름을 보내 줄 때만 " · kim".
  - `pcVersion.versions` — "v1.1.0 · 드라이버 1.0". 드라이버는 major.minor까지만, 화면(프로필) 이름은 넣지 않는다. #92: 성공한 폴링마다 `service_version`을 장치 필드(persist)에 남기고, 연결이 끊긴 동안에도 그 값을 그대로 보여 준다 — 꺼진 PC의 버전은 바뀌지 않는다. `v?`는 **한 번도 응답받지 못한** PC에만 쓴다. 업데이트 꼬리말(" · 업데이트 v1.2.0")은 기억하지 않는다. 있다/없다는 살아 있는 응답만 말할 수 있다.
  - `pcDefer.summary` — "없음" / "종료 · 4분 후"(1분 미만이면 "곧"). #89: 1시간부터는 시간으로("종료 · 2시간 후", "종료 · 1시간 30분 후"), 하루부터는 일과 시간으로("종료 · 1일 3시간 후") 읽는다 — "4320분 후"는 아무도 3일로 읽지 못한다. 누가 걸었는지는 `origin` 줄과 `lastCommand`가 말한다.
- 자동화용 조건은 `powerState`, `pcDefer.status`/`active`/`planCommand`, `pcInfo.connection`, `pcUser.locked`. 동작은 `execute`·`schedule`·`setPlanCommand`의 `multiArgCommand`다.

## 6. 드라이버 동작

### 6.1 폴링과 health

- `pollInterval` 환경설정(10초/30초/1분/5분, 기본 30초)마다 `GET /st/v1/status`. `refresh`와 모든 명령 직후에도 한 번 돈다.
- 장치가 여럿이면 DNI의 FNV-1a 해시로 시작 시각을 주기 안에 흩어, 같은 초에 모든 PC를 찌르지 않는다.
- **health는 언제나 online이다.** offline 장치는 앱이 회색으로 만들어 Wake-on-LAN을 못 쓰게 한다. PC가 꺼진 것은 `powerState`와 `switch`가 말한다.
- `429`는 아무것도 다시 칠하지 않고 넘어간다. PC는 멀쩡하고 너무 자주 물었을 뿐이다.
- **emit은 모두 `poll.emit` 한 곳을 지나고, 거기서 이벤트 예산을 지킨다**(플랫폼 노트 "이벤트 예산(rate limit)"). 플랫폼은 허브가 버리는 중복 이벤트까지 장치 예산으로 세고, 예산을 넘겨 잃은 값은 허브 캐시만 바꿔 놓아 클라우드가 굳는다. 그래서:
  - 폴링·푸시는 상태 전체를 레코드로 만들지만, `poll.emit`은 이번 실행에서 그 줄(`poll.row_key`)에 마지막으로 보낸 값과 같은 **강제 아닌** 레코드를 내보내지 않는다(`poll.SENT_FIELD`, 표는 정규화된 직렬화로 비교). 강제 레코드는 언제나 나가고, 각 줄의 실행 첫 전송은 여전히 강제다(`poll.FIRST_FIELD`). `poll.repaint`(프로필 변경·이전)는 기록을 비운다.
  - v1.2.0 명령(`run_feature`)과 프리셋의 후속 폴링은 `poll.answer`를 거친다: 첫 명령은 바로 폴링하고, 1.5초 창 안에 온 명령들은 강제할 줄을 합쳐 창이 닫힐 때 한 번만 폴링한다. 바뀐 줄과 강제 응답 줄만 나간다.
  - 10분마다(그리고 10초 안에 명령 셋 이상이면 60초 뒤) 사용자가 보는 줄 여섯을 마지막 값으로 강제 재전송한다(`poll.resync`, 폴링 끝의 `poll.resync_due`).
  - 드라이버가 10초에 20개를 넘게 내면 `log.warn`을 한 번 남긴다. 버리지는 않는다.

### 6.2 전원 상태 머신

| 이벤트 | 전이 |
|---|---|
| `status_ok` | → `on`, 실패 카운터 0 |
| `unreachable` | `waking`이면 유지, `sleeping`/`hibernated`면 유지, 그 밖에는 **연속 2회**에서 `off`(직전 `stopping` 사유가 절전이면 그 상태 유지) |
| `stopping`(reason) | `suspend`→`sleeping`, `hibernate`→`hibernated`, `shutdown`/`restart`/기타→`shuttingDown` |
| `switch_on` | → `waking`, 직전 상태를 기억 |
| `wake_timeout` | `waking`이면 기억해 둔 직전 상태로 |
| `schedule_cancelled` | `shuttingDown`이면 → `on` (유예 취소가 스위치를 되살린다) |

`switch`는 상태에서 파생된다: `on`·`waking`·`shuttingDown`이면 켜짐, 나머지는 꺼짐.

### 6.3 푸시

- 드라이버당 리스너 **하나**를 임의 포트에 열고 `POST /pc/evt`만 받는다. 첫 장치의 `init`에서 시작한다.
- 콜백에 쓸 허브 IP는 `driver:get_ip()`, 없으면 PC 쪽으로 UDP 소켓을 connect 해서 커널이 고른 출발 주소를 읽는다(패킷은 보내지 않는다).
- 구독은 폴링이 성공할 때마다 확인하고 **TTL의 80%**에 갱신한다. 실패하면 조용히 폴링만으로 동작한다.
- 들어온 이벤트는 파싱 즉시 `200`으로 답하고 나서 적용한다. 최상위 `machine_id`로 장치를 찾고, 모르는 것은 버린다.
- 적용 경로는 폴링의 후반부와 같다: `type`이 상태 머신을 움직이고, 실린 `status`가 `state.apply_status`를 통과한다.
- 드라이버가 내려갈 때(`driver_lifecycle` shutdown) 모든 구독을 해지하고 리스너를 닫는다.

### 6.4 Wake-on-LAN

- `switch on`과 `execute(wake)`는 같은 시퀀스다: 매직 패킷을 **즉시·2초 뒤·5초 뒤** 세 번, 포트 **7과 9** 양쪽으로 보낸다.
- **MAC은 세 단계로 고른다**(#97): ① `macAddress` 환경설정 — 사용자가 드라이버에 직접 넣은 값이라 무엇도 밀어내지 못한다 ② 마지막 폴링이 기억한 `wol.selected.mac`(없으면 `adapters[].selected`가 가리키는 행) — 서비스가 고른 어댑터다(§3.2) ③ 그것도 없는 옛 서비스면 예전 추측, 즉 `wol.adapters`에서 `wol_enabled`인 첫 어댑터, 아니면 MAC이 있는 첫 어댑터. ②③은 persist 한다: 깨우는 시점은 PC가 꺼져 있는 시점이라 읽을 상태 응답이 없다.
- **"WoL 꺼짐" 안내는 고른 어댑터 하나를 기준으로 한다**(#97). `wol.selected.wol_enabled`가 답이고, `selected`가 없는 옛 서비스에서만 `wol.ready`를 쓴다. 다른 랜카드에 WoL이 켜져 있다고 경고가 가려지지도, 고른 어댑터가 멀쩡한데 경고가 뜨지도 않는다. `pcInfo.message`는 어댑터 이름을 넣어 "이더넷 어댑터에 WoL이 꺼져 있습니다 · 네트워크 탭 확인"이라고 쓰고, `pcInfo.summary`는 줄이 `state.SUMMARY_MAX_CHARS`(24자) 안에 들어올 때만 "연결됨 · WoL 꺼짐 (이더넷)"까지 쓴다 — 넘치면 이름을 빼고 "연결됨 · WoL 꺼짐"으로 돌아간다(상세 줄은 말없이 잘린다, 플랫폼 노트 "화면 배치"). #102의 가동 시간은 이름보다 먼저 빠진다(§5).
- 보내는 주소는 `wolBroadcast`(기본 `255.255.255.255`). 공유기가 막으면 서브넷 브로드캐스트를 넣는다.
- 상태는 `waking`이 되고 90초 뒤에도 응답이 없으면 직전 상태로 돌아가며 "깨우기 실패"를 `pcInfo.message`에 쓴다. 그 사이 폴링이나 푸시가 성공하면 타임아웃을 취소한다.
- 어댑터의 WoL이 꺼져 있다는 것을 이미 알고 있으면 패킷은 그대로 보내되 미리 안내를 띄운다. 마지막 폴링이 어댑터 이름까지 기억해 두므로(persist) PC가 꺼져 있어도 어느 랜카드를 열어야 하는지 말해 준다.

### 6.5 검색과 식별

- **장치 추가는 SSDP 검색이 유일한 경로다. 검색 전에 PC와 PC Control 서비스가 켜져 있어야 한다**(#94). 인바운드 UDP 1900이 허브에서 PC로 닿아야 하고, 네트워크 프로필은 개인이어야 한다.
- [주변 기기 검색]에서 M-SEARCH를 보내고, 응답한 `LOCATION`마다 설명 문서를 받아 `{ip, port, machine_id, hostname, …}`을 만든 뒤 `machine_id`로 병합한다.
- **`machine_id`가 식별자다.** 이미 그 id를 가진 장치가 있으면 새로 만들지 않고 주소만 갱신한다. `ipAddress`를 손으로 채워 쓰는 장치도 첫 성공 조회에서 id를 기억하므로 나중에 검색이 같은 PC를 찾아도 중복되지 않는다.
- **장치의 `model`이 PC id를 나른다**(#94): `PC Control · <machine_id 앞 8자>`(예: `PC Control · 58bff996`). 라벨은 이름(`<호스트> 컴퓨터`)이고 모델이 id이므로, 앱의 장치 정보 화면에서 Windows 앱 SmartThings 섹션(#95)과 같은 값을 맞춰 볼 수 있다. #94 이전에 만들어진 장치는 폴링이 `machine_id`를 배우거나 확인할 때(`poll.remember_identity`) `try_update_metadata`로 한 번 갱신하고, 쓴 id를 장치 필드에 persist 해 장치당 한 번만 돈다. 허브가 거부하면 옛 모델명을 그대로 두고 다음 기회에 다시 시도한다. **실측(2026-09-26)**: 허브는 갱신을 받지만 클라우드의 `deviceModel`은 바뀌지 않았다(플랫폼 노트). 그래서 id는 실질적으로 **생성 시점에** 들어가며, 채널 공개 전 개발 장치를 지우고 다시 추가하는 §11 절차가 이 경로를 대신한다.
- **아무도 응답하지 않으면 아무것도 만들지 않는다**(#94). 예전에는 자리표시 장치(`PC Control (set IP in settings)`)를 만들었지만, 그것은 "PC가 꺼져 있다"를 장치로 굳히는 일이었고 IP가 빈 장치가 하나라도 있으면 다음 검색을 막았으며 다른 기기를 찾는 검색에도 쓸모없는 장치로 끼어들었다. 대신 `discovery_none`(한국어+영어 한 줄)을 info 로그에 남긴다.
- 검색이 안 될 때 진단 순서: ① PC와 PC Control이 켜져 있는지 → ② 인바운드 UDP 1900 방화벽 규칙 *SmartThings PC Control SSDP*(와 네트워크 프로필 개인) → ③ 앱의 SmartThings 섹션에 찍히는 **'마지막 검색 요청'** 시각 — 허브의 M-SEARCH가 PC에 닿았는지를 말해 준다(#95, 서비스 v1.1.1) → ④ `allowed_hubs` 허브 허용 목록.
- `machine_id`는 같은데 호스트 이름이 다르면(이미지 복제) 경고를 `pcInfo.message`에 띄운다.
- `ipAddress` 환경설정이 비어 있고 `followDiscovery`가 켜져 있으면 검색이 알려 온 주소를 따라간다. `unreachable`이 된 장치는 **장치당 5분에 한 번** 표적 검색을 돈다.
- `config.yml`의 `permissions`에는 `lan`과 `discovery`가 모두 필요하다.
- 검색은 **PC가 켜져 있고 PC Control이 돌고 있을 때만** 응답을 받는다. 0대가 나왔을 때 PC 쪽에서 볼 곳은 앱 [네트워크] 탭의 검색 상태 줄이고, 그 값은 `GET /api/st/hub`의 `ssdp: {running, firewall_rule, last_search: {ip, at}}`에서 온다(§3.6). 순서는 앱 켜짐 → 방화벽 규칙 → 마지막 검색 요청 시각 → 허브 allow list.

### 6.6 프로필 이전

- 프레젠테이션이나 capability 목록이 바뀌면 프로필 이름 버전을 올린다(`name: pc.vN`). **현재는 `pc.v5`** — 첫 공개 때 v1로 초기화했고(#90, §11), 드라이버 1.1.0의 v1.2.0 capability가 v2를 만들었고(#107), 표준 알림 두 capability를 커스텀 `pcMessage`로 바꾼 것이 v3, 읽기를 없애고 `pcNotify`로 바꾼 것이 v4, 입력 줄을 `lastMessage`에 묶은 `pcToast`로 바꾼 것이 v5다(#108, §4.1). v2·v3·v4는 Dev 채널에만 나가 파일이 패키지에 없다(`profiles.UNSHIPPED_VERSIONS`).
- 옛 프로필 파일은 패키지에 남긴다. 아직 옮겨지지 않은 장치가 참조한다. 지금은 v1의 `profiles/pc.yml`(`pc.v1`)과 `profiles/pc-<style>.yml`(`pc-<style>.v1`) 열 개이고, 고정이다.
- `init`/`added`가 `profiles.ensure`를 불러 알고 있는 옛 이름의 장치를 현재 프로필로 옮긴다(장치당 드라이버 구동 1회). 모르는 이름은 건드리지 않는다. 옮긴 이름은 이번 구동의 "현재 이름"(`profiles.current_name`)이 되므로, 허브가 `device.profile.name`을 늦게 바꿔도 같은 `init`의 아이콘 전환이 새 이름에서 출발한다. 옮겼으면 `poll.repaint_soon`.
- 이전 직후에는 capability id가 바뀌었거나 새로 생겨 모든 속성이 비어 있다. `poll.ensure_rows`가 세대 스탬프(`ROWS_VERSION`, #107에서 `"2"`, v3에서 `"3"`, v4에서 `"4"`, v5에서 `"5"`)를 보고 전 줄을 한 번 다시 칠한다.
- **두 축, 스무 개 (#107)**: 카테고리는 프로필마다 하나로 고정이라 환경설정 `iconStyle`(§7)의 값마다 프로필이 하나씩이고(#100), 배터리 카드는 배터리가 있는 PC에만 있어야 하므로(#116) 그 각각에 `battery` 컴포넌트가 있는 짝이 있다. 이름은 `pc.v5` · `pc-<style>.v5` · `pc-battery.v5` · `pc-<style>-battery.v5`(`profiles.name_for`), 파일은 이름의 `.vN`을 `-vN`으로 바꾼 `profiles/pc*-v5.yml`. 스무 개 모두 "현재"(`profiles.CURRENT`)이므로 `ensure`는 옮기지 않는다.
- **생성기**: 스무 개는 손으로 쓰지 않는다. `tools/gen-profiles.js`가 `tools/profile-template.yml` 하나에서 만든다 — 템플릿 머리 주석을 떼고, `__NAME__`·`__CATEGORY__`를 채우고, `# @battery-begin`…`# @battery-end` 사이는 배터리 변형에만 남기고, 생성 머리 주석을 붙인다. `tests/profilegen_test.lua`가 같은 규칙을 Lua로 적용해 디스크의 파일과 비교하고, 생성기 소스의 스타일·카테고리 목록과 `VERSION`이 `profiles.lua`와 같은지도 본다. 템플릿이 `profiles/` 밖에 있는 이유: 패키저는 그 폴더의 YAML을 모두 프로필로 올리므로 템플릿이 스물한 번째 프로필이 된다. media-notify.md §13이 적은 "`profiles/pc.yml` 하나에서"는 이것으로 바뀌었다 — `pc.yml`은 v1 장치가 아직 쓰는 고정 파일이다.
- **미디어 묶음의 두 배치(#118)**: 기본은 main 안이다(`# @media-begin`…`# @media-end` 사이가 제자리에 남는다). `bun tools/gen-profiles.js --media-component`는 그 줄들을 main에서 빼 `# @media-component` 자리에 컴포넌트 `media`(label "미디어")로 옮긴 대안 배치를 쓴다 — Dev 채널에서 두 화면을 비교하려고 패키징할 때만 쓰고 **커밋하지 않는다**(동기 테스트는 기본 배치를 지키므로 실패한다. 비교가 끝나면 플래그 없이 다시 생성). 파일 머리의 `media group: main` / `component media`가 어느 쪽인지 말한다. 두 배치는 프로필 이름이 같으므로 비교는 배치마다 새로 추가한 장치로 한다(이미 v5에 올라탄 장치의 화면은 굳어 있다). 드라이버는 둘 다 받는다: `poll.emit`이 미디어 묶음의 이벤트(`features.MEDIA_CAPS`)를 장치 프로필에 `media` 컴포넌트가 있으면 거기로 보내고, 명령은 컴포넌트와 무관하게 같은 핸들러가 받는다. 확정되면 템플릿을 그쪽으로 고치고 프로필 버전을 올린다.
- 스타일 전환은 `profiles.apply_style`이 `infoChanged`에서(값이 바뀌었을 때) 그리고 `init`에서(전환 전에 드라이버가 재시작된 경우) `try_update_metadata({ profile = … })`로 한다. **배터리 쪽은 그대로 둔다**(`pc-battery.v5` + 모니터 → `pc-monitor-battery.v5`). 새 프로필은 클라우드 기록이 비어 시작하므로 `poll.repaint_soon`으로 다시 칠한다. 전환이 다시 `infoChanged`를 내므로, 이번 구동에서 옮긴 이름을 기억해 `name_of` 대신 쓰고(허브가 `device.profile.name`을 늦게 바꿔도 되풀이하지 않는다), 거절된 대상은 같은 구동에서 다시 요청하지 않는다. `iconStyle`이 없는 장치와 현재 이름이 아닌 장치는 건드리지 않고, 모르는 값은 `others`로 본다.
- **버전을 올릴 때**: 템플릿의 내용, 생성기와 `profiles.lua`의 `VERSION`을 함께 올리고 생성기를 돌린다. `KNOWN`은 v1 이름 열 개, v2·v3·v4 이름 스무 개씩, 현재(v5) 이름 스무 개 순이다(`name_for`로 만든다). `migration_for(name, battery)`는 옛 이름에서 스타일을 읽어(`pc-<style>[-battery].vN`) 같은 스타일의 새 버전으로 옮긴다. 배터리 쪽은 이름에 `-battery`가 있으면 그대로(v2부터), 없으면 호출자가 정한다 — v1에는 배터리 변형이 없었으므로 `ensure`는 `BATTERY_FIELD`가 참일 때만 배터리 쪽으로 옮기고, 아니면 status를 본 뒤의 별도 이동이다(#116). 이름을 믿는 것은 그 장치가 status 두 번이 연달아 같아서야 그 프로필에 갔기 때문이다.
- **컴포넌트**: main이 아닌 컴포넌트(`awake` #115, `battery` #116)의 이벤트 레코드는 `component`를 달고 나가고, `poll.emit`이 `device.profile.components[<id>]`를 찾아 `emit_component_event`로 보낸다. 장치의 프로필에 그 컴포넌트가 없으면(아직 v1, 또는 배터리 없는 변형) 조용히 건너뛴다. 강제 줄 키는 `<component>/<cap>.<attr>`(`poll.row_key`)이다.

### 6.7 여러 PC

- 장치 하나 = PC 하나 = `machine_id` 하나. 시크릿·MAC·브로드캐스트·포트는 모두 장치별 환경설정이다.
- 푸시 리스너는 드라이버당 하나이고 `machine_id`로 라우팅한다.
- 폴링은 DNI 해시로 분산한다.

### 6.8 언어

- 프로필·프레젠테이션의 **라벨**은 번역 파일(ko/en)이 담당하고, 값 문구는 병기 문자열이다.
- 드라이버가 만드는 **문장**(`pcInfo.message`, 요약 줄, `lastCommand`, `origin`)만 `language` 환경설정을 따른다. 드라이버는 허브 로케일을 읽을 수 없으므로 `auto`는 한국어다.

### 6.9 전환 중 동작 (#93)

PC가 **전환 중**일 때는 명령 목록이 "진행 중"으로 읽히고, 추가 명령은 서비스로
나가지 않는다. 종료 유예에 들어갔거나 WoL로 켜는 중인데 목록·토글이 평소처럼
열려 있으면, 사용자가 보내는 두 번째 명령은 이미 도는 명령과 경합하거나 이미
없는 PC에 닿는다. 앱에는 줄 비활성화·로딩 상태가 없으므로(플랫폼 노트
"supportedValues") 다음 셋으로 근접시킨다.

**무엇이 전환인가** (`state.is_transitioning`)

- `powerState`가 `shuttingDown` 또는 `waking`.
- **또는** 유예 중. `switch off`(과 `default`/`grace` 모드의 `execute`)는 서비스가 유예만큼 미뤄 두므로 `executed: false`와 함께 **예약으로 돌아온다**(§3.3). 그동안 `powerState`는 여전히 `on`이고, 전환의 유일한 흔적은 곧 실행될 예약뿐이다. 그래서 **`remaining_seconds ≤ 유예 길이`이고 명령이 `shutdown`·`forceshutdown`·`restart`·`suspend`·`hibernate`인 활성 예약**을 전환으로 친다.
- **유예 길이는 서비스가 알려 준다.** status의 `grace.seconds`(§3.2, 기본 5분, 최대 30분)가 그대로 상한이다(`state.grace_limit`). 짐작하면 양쪽으로 다 틀린다 — 5분 유예를 건 PC는 앞의 3분 동안 멀쩡해 보이고, 넉넉히 잡은 고정값은 사용자가 일부러 건 짧은 예약까지 삼킨다. **같은 "4분 남음"이 5분 유예를 쓰는 PC에서는 전환이고 60초 유예를 쓰는 PC에서는 예약**이다. `grace.enabled`는 보지 않는다 — `execute(mode: "grace")`는 기본 설정과 무관하게 유예를 강제하므로 쓸모 있는 절반은 길이다.
- `grace` 블록을 보내지 않는 옛 서비스에만 고정 폴백 `state.GRACE_SECONDS`(120초)를 쓴다. 기본 60초 유예를 여유 있게 덮는 값이다.
- **사용자가 건 예약은 전환이 아니다.** 3일 뒤 종료가 걸린 PC는 평범하게 쓰는 PC다. 유예 길이 상한이 그 둘을 가른다.
- `state.remember_schedule`이 `active`·남은 초·명령·유예 길이 넷을 폴링과 푸시에서 함께 기억한다. 유예 길이는 status가 실어 줄 때만 덮어쓴다(끊긴 푸시 본문 때문에 이미 배운 값을 잃지 않는다). `schedule_cancelled`는 예약 셋을 지우므로 취소하면 전환도 끝난다.

**① 목록이 쉬는 값** — `lastAction`이 `none` 대신 `busy*`가 된다.

| 전환 | 값 | 문구 |
|---|---|---|
| `waking` | `busyWake` | 켜는 중… (Waking…) |
| 재시작(`power.stopping` reason 또는 유예 중인 명령이 `restart`) | `busyRestart` | 재시작 진행 중… (Restarting…) |
| 절전 `suspend` | `busySleep` | 절전 진행 중… (Going to sleep…) |
| 최대 절전 `hibernate` | `busyHibernate` | 최대 절전 진행 중… (Hibernating…) |
| 그 밖의 종료 | `busyOff` | 종료 진행 중… (Shutting down…) |

`busy*`는 `execute`의 인자이기도 하다 — 목록을 고르지 않고 닫으면 그 줄의 현재 값이
나가므로, `none`과 똑같이 **무동작**이어야 한다. 목록의 **명령 쪽 항목은 아니다.**
전환이 끝나면 `none`으로 돌아온다. 폴링뿐 아니라 실패한 폴링·푸시·`switch on`·깨우기
타임아웃도 이 값을 따라 움직인다 — `waking` 동안에는 폴링이 계속 실패하므로 성공
경로만으로는 줄이 멈춰 있다.

`lastAction` 재전송 규칙은 둘뿐이다(2026-09-26 실측으로 다시 좁혔다, 플랫폼 노트
"강제 이벤트 연발"):

- **쉬는 값이 바뀌면** 강제로(`state_change`) 내보내고, **다음 폴링에 한 번 더** 같은 값을 강제로 내보낸다(`poll.ACTION_CONFIRM_FIELD`). 반드시 닿아야 하는 것은 전환의 끝을 말하는 `none`이고, 실측에서 잃은 것도 그것이다. 바뀐 값을 강제해도 손해가 없다 — `state_change`는 "같아 보여도 전달하라"는 뜻일 뿐이다.
- **바뀌지 않았으면 아무것도 보내지 않는다.** 예외는 앱이 회전 표시를 띄운 채 기다리는 **명령의 응답**(`poll.answer_action`) 하나뿐이고, 그것이 갚을 재전송이 남아 있으면 같이 갚는다.

한 번의 반복을 타이머가 아니라 장치 필드로 두는 이유: `repaint_soon`의 15초·90초
후속이 같은 일을 하지만 그때마다 **모든 카드의 모든 줄**을 다시 칠하고 강제 폴링까지
돌린다. 줄 하나에 쓰기에는 너무 크고, `ensure_action`은 푸시·WoL 경로에서도 불리므로
`driver` 핸들을 받지 않는다.

**② 항목 숨김 실험** — `supportedCommands`(string 배열)를 프레젠테이션의
`supportedValues`가 읽는다. 평소에는 메뉴 여덟, 전환 중에는 지금 쉬는 `busy*`
하나뿐이다. **실기 확인 대기**이고, 먹지 않아도 ①과 ③은 그대로 성립한다. 빈
배열은 쓰지 않는다(플랫폼 노트).

**③ 드라이버 가드** — 전환 중 막히는 것과 통과하는 것:

| | |
|---|---|
| 막힌다 | `execute(<명령>)` 전부(`lock`·화면 켜기/끄기 포함 — 떠나는 PC도, 아직 뜨지 않은 PC도 못 한다), 인자 없는 명령들, `switch off`, `schedule(N>0)`, `setPlanCommand` |
| 통과한다 | `refresh`, `cancel()`, `schedule("0")`(취소)·`schedule("-1")`(무동작), `execute("none")`·`execute("busy*")`, **`switch on`과 `execute("wake")`** |

`switch on`은 언제나 통과한다. `shuttingDown` 중에는 §6.2의 "유예 취소가 스위치를
되살린다"가 바로 그 줄이고, `waking` 중에는 매직 패킷을 한 번 더 보내는 것뿐이다.
`execute("wake")`는 같은 시퀀스이므로 같이 통과한다.

막힌 명령은 ⑴ 그 명령이 들어온 줄을 **쉬는 값으로 강제 재전송**하고(그러지 않으면
앱이 회전 표시 뒤 오류로 끝난다, 플랫폼 노트), ⑵ `pcInfo.message`와
`pcInfo.summary`에 "종료 진행 중 · 끝난 뒤 다시 시도"를 띄운다. 다음 폴링이 원래
문구로 되돌린다. 서비스로는 아무것도 나가지 않는다. `setPlanCommand`는 서비스로
나가는 명령이 아니지만, 지금 돌 예약을 다시 겨누는 것도 "앱은 받았는데 아무 일도
없는" 같은 종류라 막고 줄은 **원래 값**으로 답한다.

표준 `switch` 토글은 회색 처리가 불가하므로 ③으로만 처리한다. `shuttingDown`
동안 스위치는 §6.2에 따라 여전히 `on`이므로, 거부된 끄기는 그 값을 강제로 다시
내보내 토글이 제자리로 튕겨 나간다.

## 7. 환경설정

| 이름 | 종류 | 기본 | 뜻 |
|---|---|---|---|
| `ipAddress` | string | `""` | PC의 IPv4. 비우면 SSDP가 알려 준 주소를 쓴다. 채우면 언제나 우선 |
| `followDiscovery` | bool | 켬 | SSDP가 다른 주소를 알려 오면 따라간다 |
| `port` | integer | `5001` | 서비스 명령 포트 |
| `secret` | string | `""` | `X-PC-Secret`으로 보낼 시크릿. Edge에 비밀번호 입력 타입이 없어 입력 중 보인다 |
| `macAddress` | string | `""` | WoL용 MAC. 비우면 서비스가 보고한 어댑터를 쓴다 |
| `wolBroadcast` | string | `255.255.255.255` | 매직 패킷을 보낼 주소 |
| `pollInterval` | enum | `30` | 10 / 30 / 60 / 300초 |
| `offAction` | enum | `shutdown` | 스위치를 끌 때 보낼 명령 |
| `buttonMode` | enum | `default` | 명령 목록의 유예 처리: 설정된 유예 따름 / 즉시 |
| `language` | enum | `auto` | 문구 언어(auto = 한국어) |
| `iconStyle` | enum | `others` | 장치 아이콘(카테고리). 값마다 카테고리만 다른 프로필로 갈아탄다(#100, §6.6) |
| `awakeMinutes` | integer 0–1440 | `60` | 잠들지 않기 스위치를 켰을 때의 기간(분). 0 = 끌 때까지(#115) |

`iconStyle`의 값과 프로필·카테고리(배터리 변형은 이름의 `.v5` 앞에 `-battery`, 기본 스타일은 `pc-battery.v5`):

| 값 | 프로필 | 카테고리 |
|---|---|---|
| `others` | `pc.v5` | Others |
| `monitor` | `pc-monitor.v5` | SmartMonitor |
| `switch` | `pc-switch.v5` | Switch |
| `plug` | `pc-plug.v5` | SmartPlug |
| `tv` | `pc-tv.v5` | Television |
| `projector` | `pc-projector.v5` | Projector |
| `network` | `pc-network.v5` | Networking |
| `hub` | `pc-hub.v5` | Hub |
| `theater` | `pc-theater.v5` | HomeTheater |
| `remote` | `pc-remote.v5` | RemoteController |

`Others`는 앱에서 아이콘 선택이 막히고 `Computer` 카테고리는 API가 거부하므로, 아이콘을 바꾸는 길은 카테고리를 바꾸는 것뿐이다. 각 카테고리의 실제 아이콘과 앱의 아이콘 선택 가능 여부는 Dev 채널에서 실측해 후보를 확정한다(#100).

Edge 환경설정에는 로케일별 변형이 없어 제목·설명을 "한국어 (English)"로 병기한다.

## 8. 보안

- 시크릿은 헤더로만 오가고 로그·푸시 본문에 남지 않는다. 이벤트 필드 중 시크릿과 같은 값은 전송 전에 제거한다.
- status는 시크릿의 **설정 여부**만 알린다. 시크릿이 비어 있으면 드라이버가 안내를 띄운다.
- 허브 허용 목록, 콜백 호스트 = 요청 출처 + 사설 대역 검증, 출처별 레이트 리밋.
- `description`은 무인증이지만 버전·호스트 이름·포트만 담고, 허브 접속 기록(GUI의 "연결된 허브")을 갱신하지 않는다.
- 세션 정보는 옵트인이고, 사용자 이름은 한 겹 더 옵트인이다.

## 9. 테스트

- **Go** — `st_api_test.go`(인증·허용 목록·명령 모드·예약·취소·status 스키마), `st_push_test.go`(구독 검증·TTL·연속 실패 제거·`power.stopping` 동기 전송), `st_ssdp_test.go`(M-SEARCH 파싱·응답·레이트 리밋), `firewall_test.go`.
- **Lua** — `edge/tests/run.lua`가 전 모듈을 돈다. 상태 머신 전이, status→이벤트 매핑, 오류 분류, 푸시 본문 파싱과 갱신 타이밍, WoL 패킷 바이트, 검색 판정, 프로필 이전, i18n.
  `capabilities_test.lua`는 **정의·프레젠테이션·드라이버가 서로 맞는지**를 지킨다: emit 하는 속성이 정의에 있는지, 목록의 키가 인자 스키마를 통과하는지, `state`가 bool에 묶이지 않았는지, 값 라벨이 병기인지, 상태 줄이 빈 문자열로 나가지 않는지.
  `profilegen_test.lua`(#107)는 생성기 규칙을 Lua로 돌려 `profiles/pc*-v2.yml`과 비교하고, `features_test.lua`(#107~#118)는 v1.2.0 기능의 status → 이벤트, 명령 매핑, 가드와 오류 문구, 컴포넌트 배선, 배터리 프로필 이동을 본다.
- **다시 칠하기**(`poll.repaint`)는 v1.2.0 줄을 이번 구동에서 마지막으로 읽은 status(`extras.last_status`)로 칠한다. 한 번도 읽지 못했을 때만 쉬는 기본값(잠들지 않기 `off`, 활동 `none` …)이다 — 강제로 나가는 `off`는 그 스위치를 조건으로 쓰는 루틴을 돌린다.
- 실행: `cd edge && npm test`(CI) 또는 `bun tools/lua.js tests/run.lua`. 문법 검사는 `tests/syntax.lua`.
- 실기 검증: 채널에 올린 뒤 허브에서 검색·스위치·명령·예약·취소·푸시·프로필 이전을 확인한다.

## 10. 배포

- `.github/workflows/edge.yml` — push/PR에서 Lua 테스트와 문법 검사. `edge-vX.Y.Z` 태그에서 태그와 `src/driver_version.lua`가 일치하는지 검증한 뒤 `edge:drivers:package` → `edge:channels:assign` → 릴리스 자산 첨부.
- 필요한 저장소 시크릿: `SMARTTHINGS_TOKEN`(Devices·Drivers·Channels 권한 PAT), `ST_CHANNEL_ID`.
- 드라이버와 서비스는 **따로 버전을 매긴다.** 드라이버는 채널로, 서비스는 GitHub Release로 나간다.
- capability 정의·프레젠테이션·번역은 드라이버 패키지에 들어가지 않는다. 계정에 올리는 것은 `tools/sync-capabilities.sh`(갱신)와 `tools/create-capabilities.sh`(최초 생성)다.

## 11. 정식 릴리스 전 체크리스트

1. **프로필 이름 리셋** — 최신 프로필을 `pc.v1`(파일 `profiles/pc.yml`)로 두고, 개발 중 쌓인 `pc-vN` 파일과 `profiles.lua`의 `KNOWN`을 `pc.v1`만 남긴다. 사용자에게 보이지 않는 이름표이므로 정식은 v1에서 시작한다. 개발 허브의 장치는 삭제 후 재추가한다. **첫 공개 때 pc.v1로 초기화했다(#90); 이후 화면이 바뀌면 v2부터 올린다.** `poll.ROWS_VERSION`도 같이 `"1"`로 되돌렸다 — 이전 스탬프를 가진 장치가 채널에는 없다.
2. **capability 이름 확정** — `pcPower` `pcRemote` `pcDefer` `pcUser` `pcInfo` `pcVersion` 그대로 v1. 계정에 옛 정의가 남아 있지 않은지 `smartthings capabilities`로 확인한다. 배포 후 정의 변경은 새 id로만 가능하다.
3. **버전** — `src/driver_version.lua` = `1.0.0`, 태그 `edge-v1.0.0`(CI가 일치를 검증한다).
4. **채널** — 채널 이름은 `Protomothis`(id `53831a53-…`, 드라이버 id는 그대로). 초대 링크 `https://bestow-regional.api.smartthings.com/invite/Kr2zNWYgpp2A`(만료 없음, `edge:channels:invites:create`로 생성)는 README/edge README/Wiki에 적혀 있다.
5. **서비스** — v1.1.0 정식 태그는 `milestone/v1.1.0 → develop → main → v1.1.0` 순서로 올린다.
