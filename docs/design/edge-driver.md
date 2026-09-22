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
| `src/discovery.lua` | SSDP 검색, 식별·중복 방지, 수동 추가 |
| `src/wol.lua` | 매직 패킷, 깨우기 시퀀스 |
| `src/profiles.lua` | 프로필 이름과 장치 이전 |
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
           "adapters": [ { "name": "Ethernet", "mac": "AA:BB:CC:DD:EE:FF",
                           "wol_enabled": true, "wol_capable": true } ] },
  "display": "on",
  "session": { "exposed": true, "locked": false, "idle_seconds": 1200, "user": "kim" }
}
```

- 예약이 없으면 `"schedule": {"active": false}`, 실행한 명령이 없으면 `"last_command": null`.
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

- 인바운드 **UDP 1900** 방화벽 규칙 *SmartThings PC Control SSDP*는 설치 때 만들고, `smartthings.discovery`가 켜져 있는 한 서비스가 시작할 때마다 다시 확인한다. 제거는 `uninstall`에서만 한다.

### 3.7 `config.json`의 `smartthings`

전부 핫 리로드다(저장 즉시 반영, 재시작 불필요).

| 키 | 기본 | 뜻 |
|---|---|---|
| `discovery` | `true` | SSDP M-SEARCH에 응답하고 UDP 1900 규칙을 유지 |
| `allowed_hubs` | `[]` | `/st/v1/*`를 쓸 수 있는 허브 IP. 비어 있으면 모두 허용 |
| `expose_session` | `false` | 잠금 여부·유휴 시간을 status와 푸시에 포함 |
| `expose_session_user` | `false` | 위가 켜져 있을 때 로그인 사용자 이름까지 포함 |

## 4. 장치 모델

한 장치가 한 PC다. 자식 장치는 만들지 않는다. 표준 capability `switch`와 `refresh`에
커스텀 capability 여섯을 더한다(네임스페이스 `numbersystem53811`, id는 소문자).

| capability | 속성 | 명령 |
|---|---|---|
| `pcPower` | `powerState` enum: `on` `sleeping` `hibernated` `off` `waking` `shuttingDown` `unknown` | – |
| `pcExec` | `lastAction` enum(= `execute`의 `command` enum), `lastCommand` string("종료 · SmartThings · 23:05") | `execute(command, mode?, minutes?)`, 인자 없는 `wake` `suspend` `hibernate` `restart` `shutdown` `lock` `screenOff` `screenOn` |
| `pcDelay` | `summary` string, `status` enum `idle`\|`scheduled`, `active` bool, `command` string, `remainingSeconds` int(0~259200), `executeAt` string(`HH:MM`), `origin` string, `planCommand` enum `shutdown` `restart` `suspend` `hibernate`, `minutesPick` enum `-1`(한 값) | `schedule(minutes: -1~4320, command?)`, `cancel()`, `setPlanCommand(command)` |
| `pcUser` | `exposed` bool, `summary` string, `locked` bool, `idleMinutes` int, `user` string | – |
| `pcInfo` | `summary` string, `connection` enum `ok` `unauthorized` `unreachable` `incompatible`, `serviceVersion`, `updateAvailable` bool, `wolReady` bool, `lastSeen` string, `message` string, `versions` string | – |
| `pcVersion` | `versions` string("v1.1.0 · 드라이버 1.0", 업데이트가 있으면 " · 업데이트 v1.2.0") | – |

- `execute`의 `command` enum은 서비스 명령 여덟에 `wake`와 `none`을 더한 열이다. `wake`는 서비스로 나가지 않는 WoL 시퀀스이고, **`none`은 아무것도 하지 않고 폴링만 한다** — 목록을 고르지 않고 닫으면 휴대폰이 그 줄의 현재 값을 인자로 보내기 때문이다.
- `lastAction`은 언제나 `none`에 머문다. 무엇이 실행됐는지는 `lastCommand`가 말한다.
- `schedule`의 `minutes`는 -1~4320이다(#89: 최대 3일. 정의의 범위가 바뀌었으므로 capability id도 `pcPlanner` → `pcDelay`로 바뀌었다 — 허브가 정의를 id로 캐시한다). `remainingSeconds`의 상한도 같이 259200으로 올라갔다. **`-1`은 무동작**(폴링만), **`0`은 취소**, 양수는 예약이다. 클라우드가 인자를 정의로 검증하므로 목록이 보내는 값 — 고른 값이든 닫을 때 나가는 현재 값이든 — 이 모두 정의 안에 있어야 한다.
- `minutesPick`은 `-1` 한 값뿐인 enum이고, 예약 시간 목록이 쉬는 자리다. `lastAction`이 `none`에 머무는 것과 같은 이유다 — 목록을 고르지 않고 닫으면 그 줄의 현재 값이 `minutes` 인자로 나간다. 그 전에는 이 줄이 `status`에 묶여 있어 `schedule(minutes: "idle")`이 나갔고, 클라우드가 거부해 "네트워크 오류" 팝업만 떴다.
- `pcDelay.status`는 `active`의 문자열 판이고, 이제 자동화 조건 전용이다. 목록의 `state`는 bool을 읽지 못한다.
- `pcInfo.versions`는 정의에 남아 있고 계속 emit 되지만, 화면에 그려지는 줄은 `pcVersion.versions`다.

## 5. 화면 구성

**대시보드** — `switch` 타일과 `pcPower.powerState` 값.

**상세 화면** — 앱이 상태 줄과 조작 줄을 각각 한 카드로 모은다.

| 상태 카드 | 값 |
|---|---|
| 전원 상태 | `pcPower.powerState` |
| 마지막 실행 | `pcExec.lastCommand` |
| 예약 요약 | `pcDelay.summary` |
| 세션 | `pcUser.summary` |
| 상태 | `pcInfo.summary` |
| 버전 | `pcVersion.versions` |

| 조작 카드 | 위젯 |
|---|---|
| 명령 | `pcExec.execute` 목록(깨우기·절전·최대 절전·재시작·종료·잠금·화면 끄기/켜기) |
| 예약할 명령 | `pcDelay.setPlanCommand` 목록 |
| 예약 시간 | `pcDelay.schedule` 목록(#89: 5·10·15·30·45분, 1·1.5·2·3·4·6·8·12시간, 1·2·3일, 그리고 취소). 줄이 쉬는 값은 `minutesPick`의 "시간 선택… (Pick a delay)" |

- 카드 안의 순서는 프로필의 capability 목록 순서를 따른다. 그래서 `pcVersion`이 목록 맨 끝이다.
- 라벨은 번역 파일(ko/en)의 `{{i18n…}}` 템플릿이고, **값 문구는 프레젠테이션의 `alternatives[].value`에 "한국어 (English)"로 병기**한다. 앱이 값 라벨에 번역을 적용하지 않기 때문이다.
- 모든 상태 줄은 해당 사항이 없을 때도 문구를 갖는다("없음 (None)", 예약 "없음", 세션 "꺼짐", 버전의 서비스 자리에 `v?`). 빈 문자열은 화면에서 "-"로 보인다.
- **값 문구는 줄 라벨을 되풀이하지 않는다**(#87). 라벨이 이미 "예약"·"세션"이라고 말하고 있고, 값 칸은 휴대폰이 잘라 낸다. 각 줄이 말하는 것:
  - `pcInfo.summary` — "연결됨" / "연결 안 됨 · 시크릿 불일치·응답 없음·버전 불일치" / 어댑터 WoL이 꺼져 있으면 "연결됨 · WoL 꺼짐". 시크릿 권장·업데이트 안내는 `pcInfo.message`에만 남는다(당장 할 일이 아니라 읽을 거리다).
  - `pcUser.summary` — "사용 중" / "잠김"(유휴 1분부터 " · 23분") / 노출을 끄면 "꺼짐". 서비스가 사용자 이름을 보내 줄 때만 " · kim".
  - `pcVersion.versions` — "v1.1.0 · 드라이버 1.0". 드라이버는 major.minor까지만, 화면(프로필) 이름은 넣지 않는다.
  - `pcDelay.summary` — "없음" / "종료 · 4분 후"(1분 미만이면 "곧"). #89: 1시간부터는 시간으로("종료 · 2시간 후", "종료 · 1시간 30분 후"), 하루부터는 일과 시간으로("종료 · 1일 3시간 후") 읽는다 — "4320분 후"는 아무도 3일로 읽지 못한다. 누가 걸었는지는 `origin` 줄과 `lastCommand`가 말한다.
- 자동화용 조건은 `powerState`, `pcDelay.status`/`active`/`planCommand`, `pcInfo.connection`, `pcUser.locked`. 동작은 `execute`·`schedule`·`setPlanCommand`의 `multiArgCommand`다.

## 6. 드라이버 동작

### 6.1 폴링과 health

- `pollInterval` 환경설정(10초/30초/1분/5분, 기본 30초)마다 `GET /st/v1/status`. `refresh`와 모든 명령 직후에도 한 번 돈다.
- 장치가 여럿이면 DNI의 FNV-1a 해시로 시작 시각을 주기 안에 흩어, 같은 초에 모든 PC를 찌르지 않는다.
- **health는 언제나 online이다.** offline 장치는 앱이 회색으로 만들어 Wake-on-LAN을 못 쓰게 한다. PC가 꺼진 것은 `powerState`와 `switch`가 말한다.
- `429`는 아무것도 다시 칠하지 않고 넘어간다. PC는 멀쩡하고 너무 자주 물었을 뿐이다.

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
- MAC은 `macAddress` 환경설정이 우선이고, 비어 있으면 마지막 폴링이 `wol.adapters`에서 배운 WoL 가능 어댑터의 MAC을 쓴다(persist).
- 보내는 주소는 `wolBroadcast`(기본 `255.255.255.255`). 공유기가 막으면 서브넷 브로드캐스트를 넣는다.
- 상태는 `waking`이 되고 90초 뒤에도 응답이 없으면 직전 상태로 돌아가며 "깨우기 실패"를 `pcInfo.message`에 쓴다. 그 사이 폴링이나 푸시가 성공하면 타임아웃을 취소한다.
- 어댑터의 WoL이 꺼져 있다는 것을 이미 알고 있으면 패킷은 그대로 보내되 미리 안내를 띄운다.

### 6.5 검색과 식별

- [주변 기기 검색]에서 M-SEARCH를 보내고, 응답한 `LOCATION`마다 설명 문서를 받아 `{ip, port, machine_id, hostname, …}`을 만든 뒤 `machine_id`로 병합한다.
- **`machine_id`가 식별자다.** 이미 그 id를 가진 장치가 있으면 새로 만들지 않고 주소만 갱신한다. 수동으로 추가한 장치도 첫 성공 조회에서 id를 기억하므로 나중에 검색이 같은 PC를 찾아도 중복되지 않는다.
- 아무도 응답하지 않으면 수동 설정용 장치(`PC Control (set IP in settings)`)를 하나 만든다. 단, IP가 비어 있는 장치가 이미 있으면 만들지 않는다.
- `machine_id`는 같은데 호스트 이름이 다르면(이미지 복제) 경고를 `pcInfo.message`에 띄운다.
- `ipAddress` 환경설정이 비어 있고 `followDiscovery`가 켜져 있으면 검색이 알려 온 주소를 따라간다. `unreachable`이 된 장치는 **장치당 5분에 한 번** 표적 검색을 돈다.
- `config.yml`의 `permissions`에는 `lan`과 `discovery`가 모두 필요하다.

### 6.6 프로필 이전

- 프레젠테이션이나 capability 목록이 바뀌면 프로필 이름 버전을 올린다(`profiles/pc-vN.yml`, `name: pc.vN`). 현재는 **`pc.v15`**.
- 옛 프로필 파일은 패키지에 남긴다. 아직 옮겨지지 않은 장치가 참조한다.
- `init`/`added`가 `profiles.ensure`를 불러 알고 있는 옛 이름의 장치를 현재 프로필로 옮긴다(장치당 드라이버 구동 1회). 모르는 이름은 건드리지 않는다.
- 이전 직후에는 capability id가 바뀌었을 수 있어 모든 속성이 비어 있다. `poll.ensure_rows`가 세대 스탬프(`ROWS_VERSION`)를 보고 전 줄을 한 번 다시 칠한다.

### 6.7 여러 PC

- 장치 하나 = PC 하나 = `machine_id` 하나. 시크릿·MAC·브로드캐스트·포트는 모두 장치별 환경설정이다.
- 푸시 리스너는 드라이버당 하나이고 `machine_id`로 라우팅한다.
- 폴링은 DNI 해시로 분산한다.

### 6.8 언어

- 프로필·프레젠테이션의 **라벨**은 번역 파일(ko/en)이 담당하고, 값 문구는 병기 문자열이다.
- 드라이버가 만드는 **문장**(`pcInfo.message`, 요약 줄, `lastCommand`, `origin`)만 `language` 환경설정을 따른다. 드라이버는 허브 로케일을 읽을 수 없으므로 `auto`는 한국어다.

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
- 실행: `cd edge && npm test`(CI) 또는 `bun tools/lua.js tests/run.lua`. 문법 검사는 `tests/syntax.lua`.
- 실기 검증: 채널에 올린 뒤 허브에서 검색·스위치·명령·예약·취소·푸시·프로필 이전을 확인한다.

## 10. 배포

- `.github/workflows/edge.yml` — push/PR에서 Lua 테스트와 문법 검사. `edge-vX.Y.Z` 태그에서 태그와 `src/driver_version.lua`가 일치하는지 검증한 뒤 `edge:drivers:package` → `edge:channels:assign` → 릴리스 자산 첨부.
- 필요한 저장소 시크릿: `SMARTTHINGS_TOKEN`(Devices·Drivers·Channels 권한 PAT), `ST_CHANNEL_ID`.
- 드라이버와 서비스는 **따로 버전을 매긴다.** 드라이버는 채널로, 서비스는 GitHub Release로 나간다.
- capability 정의·프레젠테이션·번역은 드라이버 패키지에 들어가지 않는다. 계정에 올리는 것은 `tools/sync-capabilities.sh`(갱신)와 `tools/create-capabilities.sh`(최초 생성)다.

## 11. 정식 릴리스 전 체크리스트

1. **프로필 이름 리셋** — 최신 프로필을 `pc.v1`(파일 `profiles/pc.yml`)로 두고, 개발 중 쌓인 `pc-v2`~`pc-v15` 파일과 `profiles.lua`의 `KNOWN`을 `pc.v1`만 남긴다. 사용자에게 보이지 않는 이름표이므로 정식은 v1에서 시작한다. 개발 허브의 장치는 삭제 후 재추가한다.
2. **capability 이름 확정** — `pcPower` `pcExec` `pcDelay` `pcUser` `pcInfo` `pcVersion` 그대로 v1. 계정에 옛 정의가 남아 있지 않은지 `smartthings capabilities`로 확인한다. 배포 후 정의 변경은 새 id로만 가능하다.
3. **버전** — `src/driver_version.lua` = `1.0.0`, 태그 `edge-v1.0.0`(CI가 일치를 검증한다).
4. **채널** — 개발용 버전을 정리하고 초대 링크를 README/Wiki의 자리표시자에 기입한다.
5. **서비스** — v1.1.0 정식 태그는 `milestone/v1.1.0 → develop → main → v1.1.0` 순서로 올린다.
