# SmartThings Edge 드라이버

PC Control 서비스를 위한 **SmartThings Edge 드라이버**(Lua 5.3)입니다. 서비스의
`/st/v1` 프로토콜로 통신하며, 그 계약은
[`docs/design/edge-driver.md`](../docs/design/edge-driver.md)에 있습니다 — 코드와
문서가 어긋나면 문서를 먼저 고칩니다.

기존 [PCControl 드라이버](https://github.com/toddaustin07/PCControl)가 스위치 하나와
ping만 제공하는 것과 달리, 이 드라이버는 서비스가 이미 알고 있는 것을 전부 SmartThings로
끌어올립니다: 정확한 전원 상태(절전·최대절전·깨우는 중·종료 대기 구분), 유예
카운트다운과 출처, 예약·취소, 연결·버전·WoL 진단, 화면 끄기/켜기, SSDP 자동 검색.

> PCControl 호환 경로(`/{secret}/{command}`)는 그대로 살아 있습니다. **기존 PCControl
> 드라이버 사용자는 아무것도 바꾸지 않아도 됩니다.** 이 드라이버는 선택지이지 교체
> 의무가 아닙니다.

---

## 요구 사항

| | |
|---|---|
| **허브** | SmartThings 허브 (Edge 드라이버를 실행할 수 있는 모델). 드라이버는 허브 안에서 로컬로 동작하며 클라우드를 거치지 않습니다 |
| **서비스** | PC Control **v1.1.0 이상**. `/st/v1` API는 v1.1.0에서 추가되었습니다. 이전 버전에 연결하면 드라이버가 `connection=incompatible`과 "서비스 v1.1.0 이상 필요" 메시지를 표시합니다 |
| **네트워크** | 허브와 PC가 **같은 서브넷/VLAN**. 드라이버는 LAN으로만 통신합니다 |
| **시크릿** | 필수는 아니지만 강력히 권장합니다. 비어 있으면 LAN의 누구나 PC를 제어할 수 있고, 드라이버는 `pcInfo.message`에 경고를 띄웁니다 |

### 방화벽과 네트워크

TCP 명령 포트(기본 5001)는 서비스 설치 시 열립니다. **SSDP 자동 검색**은 멀티캐스트라
추가 조건이 있습니다.

- **UDP 1900 인바운드**가 PC에서 허용되어야 합니다. `pc-control.exe install`이
  *SmartThings PC Control SSDP* 규칙을 추가하고, `smartthings.discovery`가 켜져 있는 한
  서비스가 시작할 때마다 규칙을 다시 확인합니다(#76). 나중에 검색을 꺼도 규칙은 남고,
  제거(uninstall) 시 삭제됩니다.
- 네트워크 프로필이 **개인(Private)** 이어야 합니다. 공용(Public) 프로필에서는 규칙과
  무관하게 Windows가 인바운드 멀티캐스트를 막습니다.
- 허브와 PC가 **같은 L2 구간**이어야 합니다(같은 서브넷/VLAN, AP 격리·클라이언트 격리
  없음). 라우터는 239.255.255.250을 구간 사이로 전달하지 않습니다.
- 가상 어댑터(Hyper-V, WSL, VPN 탭)는 시작 시
  `SSDP: <name> did not join 239.255.255.250` 로그를 남길 수 있습니다. 정상입니다 —
  응답기가 해당 어댑터를 건너뛰고 실제 LAN 어댑터만 사용합니다.

자동 검색이 끝내 아무것도 찾지 못해도 **수동 추가**로 동일하게 동작합니다.

---

## 설치

### 1. 채널 가입

드라이버는 SmartThings **채널**로 배포됩니다. 아래 초대 링크를 열고 허브를 등록한 뒤
드라이버를 설치하세요.

> **채널 초대 링크:** `(채널 생성 후 이 자리에 링크를 넣습니다)`
>
> 아직 공개 채널이 없습니다. 레포 소유자가 `smartthings edge:channels:create`로 채널을
> 만들고 초대 링크를 발급하면 이 줄을 교체합니다. 그 전까지는 아래
> [개발](#개발) 절차로 직접 패키징해 설치할 수 있습니다.

### 2. 드라이버 설치

초대 링크 → [Enroll] → 허브 선택 → **SmartThings PC Control** 드라이버 [Install].
SmartThings 앱의 *메뉴 → 설정 → 연결된 서비스 → 허브 → 드라이버*에서 확인할 수 있습니다.

### 3. 장치 추가

**자동 (SSDP, 권장)** — SmartThings 앱에서 **[+] → 기기 추가 → 주변 기기 검색**.
드라이버가 LAN에 `M-SEARCH`를 보내고, 응답한 PC마다 장치를 하나씩 만듭니다. IP·포트·
호스트 이름이 이미 채워진 채로 생성되므로 **시크릿만** 넣으면 끝입니다.

**수동** — 검색이 아무것도 찾지 못하면(서비스에서 검색을 껐거나 멀티캐스트가 막힌 망)
같은 [주변 기기 검색]이 *PC Control (set IP in settings)* 이라는 장치를 하나 만듭니다.
장치 설정을 열어 PC의 IP 주소와 시크릿을 넣으세요. 빈 장치가 쌓이지는 않습니다 — IP가
비어 있는 장치가 남아 있는 동안에는 다시 검색해도 새로 만들지 않습니다.

수동으로 추가한 장치도 첫 상태 조회에 성공하면 PC의 `machine_id`를 기억하므로, 나중에
SSDP가 같은 PC를 찾아도 중복 생성하지 않고 주소만 갱신합니다.

### 4. 환경설정

장치 설정(⋮ → 설정)에서 다음을 조정합니다.

설정 항목의 이름과 설명은 **한국어 우선(영어 병기)** 입니다(#78).

| 설정 | 설명 | 기본값 |
|---|---|---|
| `PC IP 주소 (IP address)` | PC의 IPv4 주소. **비워 두면** SSDP로 알아낸 주소를 사용합니다. 채워 넣으면 고정 IP로 간주하고 항상 이 값이 우선합니다 | `""` |
| `검색 따라가기 (Follow discovery)` | SSDP가 같은 PC를 다른 IP로 알려 오면 주소를 따라갑니다. 끄면 지금 주소에 고정됩니다 | 켬 |
| `서비스 포트 (Service port)` | 서비스의 명령 포트 | `5001` |
| `시크릿 (Secret)` | 설정 탭의 시크릿. `X-PC-Secret` 헤더로 보냅니다. **Edge에는 비밀번호 입력 타입이 없어 입력 중 화면에 그대로 보입니다** | `""` |
| `MAC 주소 (MAC address)` | Wake-on-LAN용 MAC. 비워 두면 서비스가 보고한 WoL 가능 어댑터의 MAC을 씁니다 | `""` |
| `WoL 브로드캐스트 (Broadcast)` | 매직 패킷을 보낼 주소. 공유기가 `255.255.255.255`를 막으면 서브넷 브로드캐스트(예: `192.168.1.255`)를 넣으세요 | `255.255.255.255` |
| `상태 확인 주기 (Poll interval)` | 상태 확인 주기 (10초 / 30초 / 1분 / 5분) | `30초마다` |
| `스위치 끄기 동작 (Off action)` | 스위치를 끌 때 보낼 명령. 강제 종료를 뺀 나머지는 PC에 설정된 유예를 따릅니다 | `종료` |
| `버튼 실행 방식 (Button mode)` | 상세 화면 명령 목록에서 고른 명령이 PC의 유예를 따를지(`설정된 유예 따름`) 곧바로 실행할지(`즉시 실행`). 자동화의 `명령 실행`은 자기 모드 인자를 따로 가집니다 | `설정된 유예 따름` |
| `문구 언어 (Language)` | 상태·예약 문구의 언어. 드라이버는 허브 로케일을 읽을 수 없어 자동은 **한국어**로 동작합니다 | `자동 (한국어)` |

> 푸시 구독에 성공해도 폴링 주기는 사용자가 정한 값을 유지합니다. 푸시가 즉시 반영을
> 담당하고 폴링은 안전망입니다.

---

## 사용

### 상세 화면

상세 화면은 **스위치 하나와 아홉 줄**입니다(#82, #84, #85, #86). 앱은 값만 보여
주는 줄을 한 카드에, 드롭다운을 다른 카드에 모으고, 각 묶음 안에서는 프로필의
capability 순서를 따릅니다.

```
[ 전원 스위치 ]                     켜기 = WoL, 끄기 = 스위치 끄기 동작

— 상태 줄 —
전원 상태        켜짐 (On)
마지막 실행      잠금 · SmartThings · 16:16        ← 아직 없으면 "없음 (None)"
예약 요약        종료 · 4분 남음 · SmartThings
세션             잠김 · 유휴 20분 · kim
상태             연결됨 · v1.1.0
버전             서비스 v1.1.0 · 드라이버 1.0.0 · 화면 pc.v13

— 드롭다운 —
명령             명령 선택… (Select a command) ▼  ← 고르면 바로 실행
예약할 명령      종료 (Shut down)              ▼  ← 예약이 실행할 명령
예약             예약 중 (Scheduled)           ▼  ← 5분 … 2시간 / 취소
```

- `명령` · `예약할 명령` · `예약`은 **드롭다운**입니다.
- `명령`의 항목은 깨우기 · 절전 · 최대 절전 · 재시작 · 종료 · 잠금 · 화면 끄기 ·
  화면 켜기 여덟 개입니다. **강제 종료는 화면에 없습니다.** 되돌릴 수 없는 명령이라
  자동화의 `명령 실행(execute)`에만 남겨 두었습니다.
- `명령` 줄은 고른 뒤에도 **`명령 선택…`으로 돌아옵니다.** 목록을 고르지 않고 닫으면
  앱이 그 줄의 현재 값을 명령으로 보내기 때문에(#84), 줄은 아무 일도 하지 않는 값에
  머물러 있어야 합니다. 무엇이 실행됐는지는 `마지막 실행` 줄이 말합니다.
- `예약`의 맨 아래 **취소**를 고르면 대기 중인 예약이 취소됩니다. 예전의
  [예약 취소] 버튼과 같은 동작입니다.
- 목록에서 고른 명령이 PC의 유예를 따를지는 환경설정 `버튼 실행 방식`이 정합니다.
  기본값 `설정된 유예 따름`이면 트레이 토스트·텔레그램에서 취소할 여지가 남습니다.
- 모든 줄이 **값을 가집니다**. 예약이 없으면 "예약 없음", 세션 정보가 꺼져 있으면
  그렇다고 적습니다. 값이 없는 버튼 줄은 앱이 라벨 옆에 "-"만 그려서 전부 없앴습니다.
  **빈 문자열도 똑같이 "-"로 보이므로**(#86) 아직 실행한 명령이 없으면
  `마지막 실행`이 "없음 (None)"이라고 적습니다.
- `버전`은 #86부터 **자기 카드**(`pcVersion`)에 있습니다. 한 capability 가 값 줄을
  둘 가지면 앱이 두 칸으로 나란히 그려 양쪽 다 잘리는데, `상태`와 `버전`이 그랬기
  때문입니다.
- 원시 속성(남은 초, 실행 시각, 출처, 서비스 버전, 업데이트, WoL 준비, 마지막 확인,
  유휴, 잠금, 사용자)은 화면에서 빠졌지만 **속성으로는 그대로 있어** 자동화 조건과
  이력에서 계속 쓸 수 있습니다.
- 앱 화면은 장치를 **추가한 시점의 정의**로 굳습니다. 드라이버를 올려도 바뀌지
  않으면 장치를 지우고 다시 추가하세요(드라이버가 새 프로필 `pc.v13`로 자동
  이전하지만, 이전이 막힌 허브에서는 재추가가 가장 확실합니다). 지금 어느 화면을
  쓰고 있는지는 맨 아래 **`버전`** 줄의 `화면` 값이 말해 줍니다.

### 한국어 표시

**줄 이름**(capability 라벨·속성 라벨)은 **capability translations**로 번역돼 있어,
휴대폰 언어가 한국어면 앱이 한국어로 보여 줍니다
(`capabilities/translations/<이름>.ko.json`, 영어는 `.en.json`).

**줄에 보이는 값**은 번역이 닿지 않습니다(#83 실측: 한국어 로케일에서도 `On`,
`None`이 그대로 나왔습니다). 그래서 값 문구는 프레젠테이션의 `alternatives`에
**"한국어 (English)"로 병기**해 두었습니다 — `켜짐 (On)`, `예약 없음 (No schedule)`,
`깨우기 (Wake)`처럼요. 드롭다운의 메뉴 항목도 같은 형식입니다.

드라이버가 만들어 내는 문자열 속성(요약 줄, `message`, `lastCommand`)은 환경설정
`문구 언어`를 따릅니다(`src/i18n.lua`). 두 가지는 별개입니다 — 앱 UI 라벨은 휴대폰
언어, 값 문구는 장치 설정입니다.

### 스위치

- **켜기** → Wake-on-LAN 매직 패킷(즉시 / 2초 / 5초, 포트 7과 9 모두). 상태가
  `waking`으로 바뀌고, PC가 응답하면 `on`이 됩니다. 90초 안에 응답이 없으면 이전
  상태로 돌아가고 "깨우기 실패: WoL 응답 없음"을 표시합니다.
- **끄기** → 환경설정 `Switch off action`의 명령을 보냅니다. 기본은 종료이고, PC에
  유예가 설정돼 있으면 그 유예를 따릅니다(그동안 취소할 수 있습니다).

스위치는 전원 상태가 `on` · `waking` · `shuttingDown`일 때 켜짐으로 보입니다. PC에서
유예를 취소하면 스위치가 다시 켜짐으로 돌아옵니다.

### 전원 상태

`pcPower.powerState`는 `on` · `sleeping` · `hibernated` · `off` · `waking` ·
`shuttingDown` · `unknown` 중 하나입니다. 서비스가 종료·절전 직전에 푸시를 보내므로
폴링 주기를 기다리지 않고 바뀝니다.

### 명령 실행

상세 화면의 `명령` 줄은 `pcExec.execute(command)`를 보냅니다. 고른 항목과 실제로
나가는 서비스 명령(§4.3)은 이렇습니다.

| 항목 | 보내는 서비스 명령 |
|---|---|
| 깨우기 | (서비스 호출 없음 — WoL 매직 패킷, 스위치 켜기와 같음) |
| 절전 | `suspend` |
| 최대 절전 | `hibernate` |
| 재시작 | `restart` |
| 종료 | `shutdown` |
| 잠금 | `lock` |
| 화면 끄기 | `turnscreenoff` |
| 화면 켜기 | `turnscreenon` |

`깨우기`를 뺀 전부는 `버튼 실행 방식` 환경설정의 모드로 `minutes=0` 호출을 보냅니다.

`명령` 줄 자체는 **늘 `명령 선택…`에 머뭅니다.** 목록을 열었다가 아무것도 고르지
않고 나오면 앱이 그 줄의 **현재 값을 명령 인자로 그대로 보내기** 때문입니다. 그래서
`명령 선택…`(`none`)도 정식 명령이고, 받으면 드라이버는 아무 일도 하지 않고 상태만
새로 고칩니다. 무엇이 실행됐는지는 바로 아래 **`마지막 실행`** 줄이 말합니다 —
서비스가 기록한 마지막 명령을 "잠금 · SmartThings · 16:16"처럼 보여 줍니다(앱에서
건 것이든 트레이·텔레그램에서 건 것이든 같습니다).

자동화에서는 인자를 모두 가진 `pcExec.execute(command, mode, minutes)`를 씁니다 —
명령 10종(`none` `wake` `shutdown` `forceshutdown` `restart` `hibernate` `suspend`
`lock` `turnscreenoff` `turnscreenon`; `ping`은 드라이버 내부 확인용이라 노출하지
않습니다), 모드 3종(`default` 설정된 유예를 따름 / `immediate` 유예 없이 / `grace`
유예를 강제), 분 0~1440. 화면에 없는 **강제 종료는 여기에만** 있습니다. 조건으로는
`pcExec.lastAction`과 `pcCountdown.planCommand`를 쓸 수 있습니다.

`minutes`가 0보다 크면 실행 대신 **예약**이 걸립니다(그때는 `명령` 줄이 아니라
`예약` 줄이 바뀝니다).

### 예약할 명령

`예약` 줄은 분만 고를 수 있으므로(목록 하나가 인자 하나입니다), **무엇을 예약할지**는
**예약 카드의 첫 줄인** `예약할 명령`에서 고릅니다: 종료 · 재시작 · 절전 · 최대 절전.
(#85 이전에는 이 줄이 PC 명령 카드에 있었습니다 — 앱은 줄을 그 줄을 소유한
capability의 카드에 그리므로, 예약 카드만 보면 시간만 고르는 화면이었습니다.) 고른 값은
장치에 저장되고, 명령 없이 들어온 예약이 그것을 씁니다. 아직 고르지 않았다면
`스위치 끄기 동작` 환경설정을 따르고(예약할 수 없는 값이면 종료), 자동화가
`pcCountdown.schedule(minutes, command)`로 명령을 직접 주면 그쪽이 우선합니다.

> 인자 없는 명령 8개(`wake` `suspend` … `screenOn`)도 그대로 남아 있습니다. 화면에서
> 빠졌을 뿐이고, 씬이나 옛 프로필에 남은 장치가 계속 씁니다.

### 예약과 취소

`pcCountdown`이 대기 중인 예약을 보여 줍니다. 화면에는 드롭다운 한 줄(예약 중 (Scheduled) /
예약 없음 (No schedule))과 요약 한 줄(`summary`: "종료 · 4분 남음 · SmartThings",
없으면 "예약 없음")이 나옵니다. 드롭다운이 읽는 값은 `status`(`idle`/`scheduled`)이고,
같은 사실의 bool 판인 `active`는 자동화 조건으로 남습니다. 뒤에 있는 속성
`active`, `status`, `command`, `remainingSeconds`,
`executeAt`(로컬 `HH:MM`), `origin`(앱 · SmartThings 명령 · 텔레그램 · SmartThings)은
그대로 남아 자동화 조건과 이력에서 쓸 수 있습니다. 예약은 PC당 하나이고, 새 예약은
기존 것을 대체합니다.

- `pcCountdown.schedule(minutes, command?)` — 예약합니다. 프리셋 5/15/30/60/120분이
  선택지로 나오지만 0~1440분 아무 값이나 쓸 수 있습니다. 명령을 비우면 스위치 끄기
  동작(예약할 수 없는 명령이면 종료)을 씁니다.
- **`minutes`가 0이면 취소**입니다 — 드롭다운의 `취소` 항목이 이것을 보냅니다.
  자동화에서도 `schedule(minutes: 0)`으로 취소하세요.
- `pcCountdown.cancel()` — 같은 취소이고, 정의에 그대로 남아 있습니다(자동화 액션에는
  넣을 수 없습니다). PC 쪽에는 "SmartThings에서 취소됨"으로 기록됩니다.

앱·트레이 토스트·텔레그램에서 취소해도 푸시로 즉시 반영됩니다. 반대도 마찬가지입니다.

### 상태 카드

`pcInfo`는 조용한 실패를 드러내기 위한 카드이고, 화면에 보이는 줄은 요약 한
줄(`summary`)입니다. **버전 줄은 #86부터 자기 카드(`pcVersion`)**이고 상태 줄 중
**맨 아래**에 옵니다(카드 순서는 프로필의 capability 목록 순서라, `pcinfo` 다음
`pcversion`을 목록 끝에 두었습니다). 한 capability 에 값 줄이 둘이면 앱이 두 칸으로
나란히 그려 양쪽 다 잘리기 때문에 나눈 것입니다.
버전 줄(`versions`: "서비스 v1.1.0 · 드라이버 1.0.0 ·
화면 pc.v13")의 `화면`은 이 장치가 쓰고 있는 프로필입니다 —
장치의 화면은 **추가한 시점의 정의로 굳기** 때문에, "왜 아직 옛날 화면이지?"에
답하는 것은 대개 이 값입니다. PC에 아직 연결되지 않았으면 서비스 자리에 `?`가
들어가고, 줄 자체는 그래도 나옵니다. 요약 줄은
연결이 살아 있으면 "연결됨 · v1.1.0", 끊겼으면
"연결 안 됨 · 시크릿 불일치"처럼 쓰고, 할 말이 있으면 짧은 안내를 뒤에 붙입니다
("연결됨 · v1.1.0 · 시크릿 미설정 · 설정 권장"). 전원은 바로 위 `전원 상태` 줄이
말하므로 요약에서 뺐습니다. 나머지는 속성으로 남아 자동화에서 쓰입니다.

| 속성 | 내용 |
|---|---|
| `summary` | 위 한 줄 요약(연결 · 버전 · 짧은 안내 / 연결 안 됨 · 이유) |
| `connection` | `ok` · `unauthorized`(시크릿 불일치 또는 허브가 허용 목록 밖) · `unreachable` · `incompatible` |
| `serviceVersion` | PC에서 돌고 있는 서비스 버전 |
| `updateAvailable` | 서비스에 새 릴리스가 있는지 |
| `wolReady` | WoL 가능 어댑터가 하나라도 있는지 |
| `lastSeen` | 마지막으로 성공한 상태 조회 시각 |
| `message` | 사람이 읽는 안내 **한 줄**. 요약에 붙는 짧은 형태와 달리 문장 그대로이고, 화면이 아니라 자동화·이력에서 읽습니다. 여러 개가 겹치면 오류 > 호환성 > WoL 미준비 > 업데이트 > 시크릿 없음 순으로 하나만 고릅니다 |
| `versions` | "서비스 v1.1.0 · 드라이버 1.0.0 · 화면 pc.v13". #86부터 **화면의 줄은 `pcVersion.versions`**이고 이 속성은 자동화·이력용으로 남아 같은 값을 계속 내보냅니다(정의를 지우려면 또 새 capability 이름이 필요하고, 한 번도 emit 되지 않은 속성은 앱이 "상태를 모두 보고하지 않았다"고 안내합니다) |

`pcVersion`은 그 버전 줄 하나만 가진 카드입니다.

| 속성 | 내용 |
|---|---|
| `versions` | 화면에 보이는 버전 줄. 연결 전이면 서비스 자리가 `?` |

### 세션 정보 (선택)

PC의 GUI 네트워크 탭에서 *세션 정보 노출*을 켜면 `pcUser`이 요약 한 줄
("잠김 · 유휴 20분 · kim")을 보여 줍니다. 기본은 꺼져 있고, 꺼져 있으면 그 줄이
"세션 정보 꺼짐 · PC 앱 설정에서 켤 수 있음"이라고 적습니다(속성 `exposed=false`,
나머지 값은 마지막 것이 남습니다).

> **유휴 시간은 트레이 앱이 떠 있어야 나옵니다** (#77). 서비스는 세션 0에서 돌아
> 사용자 입력 시각을 알 수 없고(`WTSINFOEXW.LastInputTime`은 Windows 10/11 콘솔
> 세션에서 사실상 업타임입니다), 사용자 세션에 있는 트레이 앱만 `GetLastInputInfo`를
> 부를 수 있습니다. 트레이 앱이 30초마다 값을 올리고 서비스는 90초 이내의 값만
> 싣습니다. 트레이 앱이 꺼져 있으면 유휴 시간은 비고, **잠금 여부와 사용자 이름은
> 영향을 받지 않습니다**(WTS에서 직접 옵니다). 유휴 변화는 푸시로 나가지 않으므로
> 폴링 주기만큼 늦게 반영됩니다.

### 자동화 예시

**1. 자정 취침 예약** — 잊고 켜 둔 PC를 30분 뒤 종료하되 취소할 여지를 둡니다.

```
조건(If)  : 시각이 00:00이고
            PC의 Power state 가 on
동작(Then): PC 의 pcCountdown.schedule(minutes: 30, command: shutdown)
```

30분 카운트다운이 SmartThings·트레이 토스트·텔레그램에 동시에 뜨고, 어디서든 취소하면
모든 곳에서 사라집니다.

**2. 외출하면 잠그고 화면 끄기** — 종료까지는 하지 않습니다.

```
조건(If)  : 구성원 전원이 집을 떠남
동작(Then): PC 의 pcExec.execute(command: lock, mode: immediate)
            PC 의 pcExec.execute(command: turnscreenoff, mode: immediate)
```

**3. 책상 주변기기 전원 연동** — 모니터·스피커 스마트플러그를 PC에 맞춥니다.

```
조건(If)  : PC의 Power state 가 waking 또는 on 으로 바뀜
동작(Then): 책상 플러그 켜기

조건(If)  : PC의 Power state 가 off 또는 sleeping 으로 바뀜
동작(Then): 책상 플러그 끄기
```

`waking`을 조건에 넣으면 WoL로 깨우는 동안 모니터가 미리 켜져 부팅 화면을 놓치지 않습니다.

---

## 여러 PC

같은 LAN에 서비스를 설치한 PC가 여러 대 있어도, 허브 하나가 여러 PC를 다루거나 여러
허브가 한 PC를 다루어도 동작합니다(설계 문서 §13).

- **식별자는 `machine_id`**(Windows MachineGuid)입니다. SSDP·상태·푸시 본문 최상위에
  모두 실려 있어 드라이버가 이 값으로 장치를 찾습니다. 같은 `machine_id`를 이미 가진
  장치가 있으면 새로 만들지 않고 주소만 갱신합니다.
- **시크릿·MAC·브로드캐스트 주소는 장치별 환경설정**입니다. 서브넷이 다른 PC에는 그
  서브넷의 브로드캐스트 주소를 넣으세요.
- **DHCP로 IP가 바뀌어도** `Follow discovery`가 켜져 있으면 따라갑니다. `PC IP address`를
  채워 넣으면 고정으로 간주하고 SSDP가 알려 주는 주소를 무시합니다. 연결이 끊긴 장치는
  다음 폴링 전에 한 번(최대 5분에 한 번) 표적 검색으로 주소를 다시 찾습니다.
- **폴링은 분산됩니다.** 장치가 N개면 장치 ID 해시로 시각을 흩어 같은 초에 몰리지
  않게 합니다. 푸시 리스너는 드라이버당 하나이고, 구독은 PC별로 하나입니다.
- **MachineGuid 복제 주의.** 한 PC를 이미지로 복제하면 두 대의 `machine_id`가 같아져
  SSDP에서 한 장치로 합쳐집니다. 드라이버는 "같은 machine_id, 다른 hostname"을 만나면
  `pcInfo.message`로 경고합니다. 해결은 한쪽에서 MachineGuid를 재생성하는 것입니다
  (Sysprep, 또는 `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid` 직접 교체).
- **텔레그램은 이 드라이버와 무관**합니다. PC 서비스의 텔레그램은 **봇 하나 = PC 하나**
  전제로 유지됩니다. 여러 PC가 같은 봇 토큰을 쓰면 `getUpdates`가 409 Conflict를 내고
  서비스가 로그·GUI로 경고합니다. **PC마다 봇을 따로 만드세요.** 여러 PC를 봇 하나로
  묶는 것은 별도 프로젝트인 허브 에이전트의 몫입니다
  ([`docs/design/hub-agent.md`](../docs/design/hub-agent.md)).

---

## 개발

### 저장소 구성

```
config.yml               드라이버 메타 (name, packageKey, permissions: lan)
profiles/pc-v13.yml      메인 프로필(현행): capability + 환경설정 (§5.1, §5.4)
profiles/pc.yml … pc-v12.yml  메인 프로필 v1~v12: 이전 장치용으로 남겨 둠 (#79)
capabilities/            커스텀 capability 정의 + 프레젠테이션 (§5.1, §5.3)
  translations/          capability 번역 ko/en (#78)
src/
  init.lua               진입점: lifecycle 과 capability 핸들러만
  caps.lua               커스텀 capability id, NAMESPACE 상수 한 곳
  client.lua             /st/v1 HTTP 클라이언트, 오류 분류 (§4, §6.1)
  discovery.lua          SSDP 검색, 수동 추가, 다중 PC 식별 (§4.6, §13)
  poll.lua               폴링 타이머, health, 이벤트 발행, 분산 (§6.1, §13.3)
  push.lua               푸시 리스너와 구독 (§4.5, §6.4)
  state.lua              순수 함수: status JSON → 이벤트, 전원 상태 머신 (§6.2)
  wol.lua                매직 패킷, 깨우기 시퀀스 (§6.3)
  i18n.lua               사용자에게 보이는 속성 문자열의 ko/en (§6.5)
  profiles.lua           프로필 이름·버전과 기존 장치 이전 (§14.3, #79)
  driver_version.lua     드라이버 버전 (User-Agent, 릴리스 태그 검증)
tests/
  run.lua                테스트 러너
  syntax.lua             모든 모듈을 실행 없이 컴파일
  helpers.lua            단언문과 가짜 장치
  mocks/                 log, ltn12, cosock, st.json, st.utils, st.driver, st.capabilities
  *_test.lua
tools/
  lua.js                 fengari 기반 `lua <file>` 러너
  apply-namespace.js     네임스페이스 일괄 적용
  create-capabilities.sh 커스텀 capability 6종 생성 (CLI, 계정당 한 번)
  sync-capabilities.sh   정의·프레젠테이션·번역 갱신 (CLI, 바꿀 때마다)
```

허브에서는 `src/`가 패키지 루트라, 모듈끼리는 항상 이름만으로 require 합니다
(`require "state"`, `require "src.state"`가 아님).

### 프로필 버전 (#79)

장치의 화면 정의는 **생성 시점**의 capability 프레젠테이션으로 굳어집니다. 같은 이름의
프로필(`pc.v1`)을 고쳐 다시 올리면 환경설정은 바뀌지만 상세 화면은 옛 것 그대로입니다
(실측, 설계 §14.3).

**프레젠테이션을 바꿀 때는 프로필 버전을 올립니다.** 기존 장치는 드라이버가 자동으로
옮기므로 삭제·재추가가 필요 없습니다.

1. `profiles/pc-vN.yml`을 새로 만들고 `name: pc.vN`으로 바꿉니다.
   **옛 파일은 지우지 않습니다** — 아직 옮겨지지 않은 장치가 참조합니다.
2. `src/profiles.lua`의 `PC`를 새 이름으로 올리고 `KNOWN`에 추가합니다.
   프로필 이름은 여기 한 곳에만 있습니다(`discovery.PROFILE`이 여기서 읽습니다).
3. 패키징해서 올리면, 드라이버가 각 장치의 첫 `init`에서 `try_update_metadata`로
   새 프로필로 옮기고 `migrated <id> to pc.vN`을 남깁니다(장치당 한 번, 실패해도
   드라이버는 계속 돕니다).

`device.profile`에 이름이 없는 펌웨어가 있어, 생성 시점 프로필 이름을
`profile_name` 필드에 영구 저장하고 필드가 없으면 `pc.v1`(=#79 이전 장치)로 봅니다.

### 테스트

설치할 Lua 인터프리터가 없습니다. [fengari](https://fengari.io/)는 JavaScript로 구현된
Lua 5.3이고, 허브와 같은 방언을 실행합니다.

```sh
cd edge
npm install                        # 또는: bun install
npm test                           # 또는: bun tools/lua.js tests/run.lua
npm run syntax                     # 또는: bun tools/lua.js tests/syntax.lua
```

`package.json`만 커밋하고 락파일은 두지 않습니다. 의존성이 fengari 하나뿐이고
정확한 버전으로 고정돼 있어서, CI도 `npm ci`가 아니라 `npm install`을 씁니다.

`run.lua`는 `tests/*_test.lua`를 찾아 각 파일이 돌려주는 테이블의 `test_*` 함수를 모두
호출합니다. 테스트 파일을 추가할 때 등록할 곳은 없습니다. 허브 모듈(`st.*`, `cosock`,
`ltn12`)은 로컬에 없으므로 `tests/mocks/`에서 preload 되고, 소켓을 건드릴 만한 것은
모두 주입받습니다(`client.get_status(device, { http = fake })`,
`wol.send(mac, broadcast, { socket = fake })`). `cosock.asyncify`는 테스트에서 일부러
예외를 던져 실제 HTTP 호출이 새어 나가지 못하게 합니다.

로직의 핵심인 `state.lua`는 순수 함수입니다 — 이벤트를 내보내는 대신
`{ cap, attr, value }` 레코드를 돌려주므로 상태 매핑과 전원 상태 머신 전체를 허브 없이
검증할 수 있습니다.

`tools/lua.js`는 `fs`·`path`만 쓰는 평범한 CommonJS입니다(bun 전용 API 없음). 그래서
CI의 node와 로컬의 bun에서 같은 명령이 그대로 돕니다.

### 커스텀 capability와 네임스페이스

`capabilities/`에 설계 문서 §5.1의 커스텀 capability 6종이 각각 정의 + 프레젠테이션
두 파일로 들어 있습니다.

```
pcPower.json               정의        -> smartthings capabilities:create -i <file>
pcPower.presentation.json  프레젠테이션 -> smartthings capabilities:presentation:create
```

`pcPower`는 전원 상태, `pcExec`은 명령 실행·마지막 실행, `pcCountdown`은 예약 표시·조작과
예약할 명령, `pcInfo`는 연결·메시지 카드, `pcVersion`은 버전 줄 하나뿐인 카드(맨 아래),
`pcUser`은 선택 항목인 잠금·유휴 블록입니다.

`src/caps.lua`, `profiles/pc.yml`, `capabilities/*.json`은 계정에 발급된 **실제
네임스페이스 `numbersystem53811`** 을 씁니다(2026-09-22 생성). SmartThings는 id의 이름 부분을 소문자로 바꾸므로 id는 `numbersystem53811.pcpower`처럼 소문자입니다. 다른 계정에서 다시 만들면 네임스페이스가 달라지며, 그때는 `tools/apply-namespace.js`로 다시 반영합니다. 원래 네임스페이스는 소유자가 커스텀
capability를 만들 때 SmartThings가 발급합니다.

```sh
cd edge
./tools/create-capabilities.sh            # 6종 생성 + 프레젠테이션, 네임스페이스 출력
node tools/apply-namespace.js <namespace> # 모든 파일에 일괄 반영 (bun 도 가능)
npm test
```

`apply-namespace.js`는 현재 네임스페이스를 `src/caps.lua`에서 읽어 바꾸므로 **여러 번
돌려도 안전하고**, 나중에 다른 계정으로 옮길 때도 그대로 쓸 수 있습니다.
`--dry-run`으로 바뀔 파일만 먼저 볼 수 있습니다. 8~20자 소문자·숫자가 아닌 값은
거부합니다.

`create-capabilities.sh`는 **계정당 한 번만** 실행하세요. `capabilities:create`에는
"있으면 갱신" 모드가 없어, 다시 돌리면 같은 이름의 capability가 하나 더 생깁니다.

정의·프레젠테이션·번역을 고친 뒤에는 `sync-capabilities.sh`로 한 번에 올립니다.
capability가 `status: proposed`인 동안에만 됩니다(공개하면 버전을 올려야 합니다).

```sh
cd edge
./tools/sync-capabilities.sh --dry-run   # 실행할 CLI 명령만 출력
./tools/sync-capabilities.sh             # 6종 × (정의 · 프레젠테이션 · ko · en)
```

### capability 번역

`capabilities/translations/<이름>.<태그>.json`이 capability 라벨, 속성 라벨, enum 값,
명령·인자 라벨을 로케일별로 담습니다. 프레젠테이션의 라벨은 영어로 두고 이 번역이
휴대폰 언어에 맞춰 덮어씁니다.

```sh
smartthings capabilities:translations:upsert <id> --capability-version 1 \
  -i capabilities/translations/pcExec.ko.json
smartthings capabilities:translations <id> --capability-version 1 ko   # 읽어서 확인
```

`tests/capabilities_test.lua`가 두 태그(ko/en) 모두에 대해 **정의에 있는 모든 속성·
enum 값·명령·명령 인자**가 번역돼 있는지, 정의에 없는 것을 번역하지 않는지, 한국어
파일에 실제로 한글이 들어 있는지를 검사합니다.

`tests/capabilities_test.lua`는 JSON과 Lua의 아귀도 맞춰 검사합니다(같은 id, `state`가
쓰는 것과 같은 속성, `init.lua` 핸들러와 같은 명령, 정의되지 않은 것을 참조하지 않는
프레젠테이션, 명령 목록 8개의 순서, 상세 화면에 `pushButton`이 하나도 없고 모든
`list`가 `state.alternatives`를 가진다는 것, `visibleCondition`을 쓰지 않는다는 것).
다만 SmartThings가 이 파일들의 **형식**을 받아들일지는 CLI만이 압니다. 올릴 때 확인할
부분:

- `pcVersion`은 **#86에서 새로 만드는 capability**입니다(버전 줄 전용). 개명이 아니라
  추가이므로 지울 옛 id 는 없고, `capabilities:create` +
  `capabilities:presentation:create` + 번역 upsert(ko/en) 뒤 프로필 `pc.v13`을
  배포하면 끝입니다. `pcInfo.versions` 정의는 그대로 둡니다.
- `pcExec`·`pcCountdown`·`pcInfo`는 **셋 다 새 capability**입니다(#85: `schedule`의
  `minutes` 최소값이 0, `planCommand`·`setPlanCommand`가 예약 쪽으로 이동,
  `versions` 속성 추가). `capabilities:update`가 아니라 `capabilities:create` +
  `capabilities:presentation:create` + 번역 upsert로 올린 뒤, 프로필 `pc.v13`가
  배포돼 옛 `...pcplan`·`...pcrun`·`...pchealth`를 참조하는 장치가 남지 않은 것을
  보고 `smartthings capabilities:delete <namespace>.pcplan`(그리고 `.pcrun`,
  `.pchealth`) 합니다. 허브가 정의를 id 단위로 캐시하기 때문에 이름을 바꾸는
  것입니다(설계 §14.4). `pcControl`→`pcAction`(#82), `pcTimer`→`pcPlan`(#83),
  `pcAction`→`pcRun`(#84)도 같은 이유였습니다.
- `pcCountdown.schedule`의 `minutes`는 정의상 최소 **0**입니다. 상세 화면의 `취소`
  항목이 0을 보내는데, **인자 검증은 클라우드가 정의로 하고 거부하면 허브에 닿지도
  않기 때문입니다**(최소 1이던 동안 "시스템 오류" 팝업만 떴습니다, 설계 §14.5).
  목록의 모든 키는 정의의 인자 범위 안에 있어야 합니다.
- `capabilities:translations:upsert`가 받는 본문 형식
  (`{"tag","label","attributes":{...,"i18n":{"value":{...}}},"commands":{...}}`).
- 정의의 `id`, `version`, `status`, `ephemeral` — CLI가 무시하거나 거부하고 자기 값을
  넣을 수 있습니다.
- 모든 속성에 `enumCommands: []`를 명시했습니다(enum이 아닌 속성 포함).
- `automation.actions`의 `multiArgCommand`와 인자별 `name`은 §14에서 실제로 통과한
  형식입니다. `pushButton`은 `automation.actions`에 넣을 수 없습니다(상세 화면 전용).
- 환경설정 `title`의 길이 제한(36자)에 한국어 병기 제목이 걸리지 않는지 —
  `edge:drivers:package`가 거부하면 영어 괄호를 줄이세요.

### CLI 패키징

```sh
smartthings edge:drivers:package edge/          # 업로드하고 driverId/version 출력
smartthings edge:drivers:package edge/ --build-only driver.zip   # zip 만 만들기
smartthings edge:channels:assign <driverId> <version> --channel <channelId>
smartthings edge:drivers:install <driverId> --hub <hubId>        # 채널 등록 후
```

로그는 `smartthings edge:drivers:logcat <driverId> --hub-address <허브IP>`로 봅니다.

**CI** — [`.github/workflows/edge.yml`](../.github/workflows/edge.yml)이
`develop` · `main` · `milestone/**` push와 `edge/**`를 건드린 PR마다 테스트와 문법
검사를 돌립니다(push에 경로 필터를 걸지 않는 이유는 워크플로 주석 참고 — 경로 필터는
태그 push에도 적용되어 릴리스가 조용히 건너뛰어질 수 있습니다). `edge-vX.Y.Z` 태그를 밀면 태그와
`src/driver_version.lua`의 문자열이 같은지 확인한 뒤 패키징 → 채널 배정 → zip을 릴리스에
첨부합니다. `-rc`가 붙은 태그는 프리릴리스로 올라갑니다. 저장소 시크릿
`SMARTTHINGS_TOKEN`(PAT)과 `ST_CHANNEL_ID`가 필요합니다.

### 온허브 검증 체크리스트

로컬 테스트로는 확인할 수 없는, 허브 런타임에서만 드러나는 것들입니다. 실기에서 한 번씩
짚어 보세요.

**런타임 가정**

- [ ] `cosock.socket.tcp()`를 `0.0.0.0:0`에 바인딩하고 `getsockname()`으로 얻은 포트에
      LAN이 도달하는지. `cosock.spawn` 안의 `accept()`가 다른 태스크를 굶기지 않는지.
- [ ] 허브 IP: `driver:get_ip()`가 있는 펌웨어인지, 없으면 PC를 향해 `setpeername`한
      UDP 소켓의 `getsockname()` 폴백이 맞는 주소를 주는지. (서비스가 콜백 호스트와
      요청 출처 IP가 같기를 요구하므로, 틀리면 subscribe가 `400`으로만 나타납니다.)
- [ ] 멀티캐스트: 허브가 Edge 드라이버의 239.255.255.250:1900 송신과, 같은 소켓으로
      오는 유니캐스트 응답 수신을 허용하는지.
- [ ] (#81) 옛 드라이버가 만든 디스플레이 자식이 첫 `init`에서 지워지는지
      (로그 `removing legacy display child <id>`). `try_delete_device`가 device에
      있는지 driver에 있는지 양쪽 다인지.
- [ ] `device:set_field(..., { persist = true })`가 드라이버 재시작 뒤에도 검색한
      주소와 `machine_id`를 유지하는지.

**기능 확인**

- [ ] SSDP로 PC가 발견되고 IP·포트·호스트 이름이 채워진 채 장치가 생기는지.
- [ ] 시크릿만 넣으면 `connection=ok`, 틀리면 `unauthorized`가 되는지.
- [ ] 스위치 끄기 → 유예 카운트다운이 SmartThings·토스트·텔레그램에 동시에 뜨는지.
- [ ] 토스트에서 취소 → SmartThings 예약 카드가 즉시 사라지고 스위치가 켜짐으로 복귀.
- [ ] 절전 → `sleeping`, 종료 → `shuttingDown` → `off`, WoL → `waking` → `on`.
- [ ] 상세 화면 `명령` 목록의 화면 끄기·켜기가 실제로 화면을 끄고 켜는지.
- [ ] PC를 껐다 켜도(서비스 재시작) 구독이 되살아나는지.
- [ ] PC 두 대를 추가했을 때 서로 섞이지 않는지.
- [ ] 커스텀 capability가 없는 상태(플레이스홀더 네임스페이스)에서도 스위치·refresh·
      health가 살아 있는지.
- [ ] (#82) 상세 화면이 스위치 + 아홉 줄(상태 줄 여섯 · 드롭다운 셋)로 나오고, 어느 줄에도 "-"가 없는지.
- [ ] (#82) `명령` 목록에 8개가 위 순서대로 나오고 강제 종료는 없는지. 하나 고르면
      실행되고 그 줄이 고른 명령으로 바뀌는지. 아무것도 실행하지 않은 장치가 `—`인지.
- [ ] (#82) `예약` 목록의 프리셋이 예약을 걸고, `취소`가 예약을 지우는지
      (허브가 `minutes: 0`을 인자 스키마로 거부하지 않는지).
- [ ] (#78) 휴대폰 언어가 한국어일 때 capability 라벨·enum 값이 한국어인지,
      영어로 바꾸면 영어가 되는지. 목록의 **항목 이름은 영어**로 남습니다(명령 인자의
      enum 값은 번역 API가 받지 않습니다 — 설계 §14.2).
- [ ] (#78) `버튼 실행 방식`을 `즉시 실행`으로 두면 목록에서 고른 명령이 유예 없이
      바로 실행되는지.
- [ ] (#79) 드라이버를 올린 뒤 기존 장치가 `pc.v13`으로 옮겨지고(로그
      `migrated ... to pc.v13`) 상세 화면이 새 프레젠테이션으로 다시 그려지는지.
      환경설정 값이 이전 뒤에도 남아 있는지.
- [ ] (#86) `버전` 줄이 자기 카드에서 **전체 폭**으로 나오고 `상태` 줄과 나란한 두
      칸이 아닌지. 두 줄 다 잘리지 않는지.
- [ ] (#86) 어느 줄도 "-"가 아닌지. 특히 아직 아무 명령도 실행하지 않은 장치의
      `마지막 실행`이 "없음 (None)"인지.
- [ ] (#86) 예약이 없는 상태에서 `예약` 목록의 `취소`를 고르면 회전 표시가 **오류
      없이** 끝나는지. `예약할 명령`에서 지금과 같은 값을 다시 골라도 마찬가지인지.

---

## 문제 해결

| 증상 | 확인할 것 |
|---|---|
| **검색해도 아무것도 안 나옴** | PC의 GUI 네트워크 탭에서 *자동 검색(SSDP) 허용*이 켜져 있는지. UDP 1900 방화벽 규칙과 네트워크 프로필(개인). 허브와 PC가 같은 서브넷인지. Wi-Fi AP 격리. 안 되면 수동 추가로 넘어가세요 |
| **`connection = unauthorized`** | 장치 설정의 시크릿과 PC 설정 탭의 시크릿이 같은지. 메시지가 "허브가 허용 목록에 없습니다"이면 시크릿이 아니라 GUI의 *허용 목록*이 문제입니다 — [현재 허브 추가]를 누르거나 목록을 비우세요 |
| **`connection = unreachable`** | PC가 켜져 있고 서비스가 돌고 있는지(`pc-control.exe status`). IP·포트가 맞는지. TCP 5001 방화벽. IP가 바뀌었다면 `PC IP address`를 비우고 `Follow discovery`를 켜세요 |
| **`connection = incompatible`** | 서비스가 v1.1.0 미만입니다. 서비스를 업데이트하세요. 반대로 드라이버가 낡았다는 메시지면 채널에서 드라이버를 업데이트합니다 |
| **스위치 켜기가 안 먹음(WoL)** | `wolReady`가 false면 PC 어댑터의 Wake-on-LAN이 꺼져 있습니다(장치 관리자 → 전원 관리). 공유기가 `255.255.255.255`를 막으면 `WoL broadcast address`를 서브넷 브로드캐스트로. 절전이 아니라 완전 종료 상태라면 메인보드의 "PCIE로 깨우기"와 빠른 시작(Fast Startup) 해제도 필요합니다 |
| **상태가 늦게 갱신됨** | 푸시 구독이 실패하고 폴링만 도는 상태일 수 있습니다. `lastSeen`을 보고, 폴링 주기를 줄여 보세요. 허브 IP 판단이 틀리면 서비스 로그에 subscribe `400`이 남습니다 |
| **장치가 두 개로 보임** | 수동 추가 뒤 SSDP가 같은 PC를 다시 찾은 경우입니다. 첫 상태 조회에 성공해야 `machine_id`를 학습하므로, 시크릿을 넣어 `ok`로 만든 뒤 남는 쪽을 지우세요 |
| **PC 두 대가 한 장치로 합쳐짐** | 이미지 복제로 MachineGuid가 같습니다. 한쪽에서 재생성하세요(위 [여러 PC](#여러-pc)) |
| **명령을 보내면 회전 표시 뒤에 오류가 뜸** | 값이 바뀌지 않는 명령을 보냈을 때 앱이 그렇게 끝납니다(예약이 없는데 `취소`, `예약할 명령`에서 지금과 같은 값 재선택, `명령` 목록을 그냥 닫기). 드라이버는 이런 응답을 `state_change`로 강제해 회전 표시를 끝내므로(#86), 그래도 오류가 보이면 장치가 아직 옛 프로필(`버전` 줄의 `화면` 값이 `pc.v13`이 아님)이거나 드라이버가 옛 버전입니다. PC에는 아무 영향이 없으니 실행 여부는 `마지막 실행`·`예약 요약` 줄로 확인하세요 |
| **줄에 "-"만 보임** | 값이 한 번도 보고되지 않았거나 빈 문자열인 줄입니다. 드라이버가 장치를 추가할 때와 첫 폴링에 모든 줄을 한 번 칠하므로(#85, #86), 남아 있다면 폴링이 한 번도 성공하지 못한 것입니다 — `상태` 줄부터 확인하세요 |
| **`상태`와 `버전`이 두 칸으로 잘려 보임** | #86 이전 화면입니다. 장치가 `pc.v13`으로 옮겨지면 `버전`이 자기 카드에서 전체 폭으로 나옵니다. 이전이 막힌 허브라면 장치를 지우고 다시 추가하세요 |
| **커스텀 타일이 안 보임** | 네임스페이스가 아직 플레이스홀더입니다. `create-capabilities.sh` → `apply-namespace.js` → 재패키징 순서로 처리하세요. 그동안에도 스위치·새로고침은 동작합니다 |
| **모니터 장치가 사라짐** | #81에서 제거했습니다. 본체 상세 화면의 [화면 끄기]·[화면 켜기] 버튼을 쓰세요. 허브에 남아 있던 자식 장치는 드라이버가 처음 뜰 때 지웁니다 |
| **텔레그램이 다른 PC 것과 섞임** | 봇 토큰 하나를 여러 PC가 공유하고 있습니다(409 Conflict). PC마다 봇을 분리하세요 |

드라이버 로그: `smartthings edge:drivers:logcat <driverId> --hub-address <허브IP>`.
서비스 로그: exe 옆 `service.log`, 또는 데스크톱 앱의 로그 탭.

---

## English

An Edge driver (Lua 5.3) for the PC Control service. It speaks the service's
`/st/v1` protocol, specified in
[`docs/design/edge-driver.md`](../docs/design/edge-driver.md) (Korean) — the
binding contract for both sides.

Where the [PCControl driver](https://github.com/toddaustin07/PCControl) offers a
switch and a ping, this one surfaces what the service already knows: a real
power state (`on` / `sleeping` / `hibernated` / `off` / `waking` /
`shuttingDown`), the grace countdown with its origin, schedule and cancel,
connection/version/WoL diagnostics and SSDP discovery.
The legacy `/{secret}/{command}` path is untouched, so **existing PCControl
users need to change nothing**.

**Requirements** — a SmartThings hub, PC Control service **v1.1.0 or newer**
(that is where `/st/v1` appears), hub and PC on the same subnet, and a secret
(optional but strongly recommended). SSDP additionally needs inbound **UDP 1900**
allowed on the PC — `pc-control.exe install` adds the rule and the service
re-checks it at every start while discovery is on — a **Private** network
profile, and no AP/client isolation between hub and PC.

**Install** — join the distribution channel (invite link to be added once the
channel exists), install the driver onto the hub, then **Add device → Scan
nearby**. SSDP fills in IP, port and hostname, so only the secret is left to
type. Without SSDP the same scan creates one device labelled *PC Control (set IP
in settings)* for you to fill in by hand; it adopts the PC's `machine_id` on its
first successful poll, so a later SSDP hit updates it rather than duplicating it.

**Preferences** — `ipAddress` (empty = follow SSDP), `followDiscovery`, `port`
(5001), `secret` (sent as `X-PC-Secret`; Edge has no password field, so it is
visible while typing), `macAddress`, `wolBroadcast`, `pollInterval`
(10 s/30 s/1 min/5 min), `offAction`, `buttonMode` and
`language`. Their titles and descriptions are Korean first with the English term
in parentheses: a profile preference has no per-locale variant, and this project
is Korean-first.

**Use** — the detail view is the switch and nine rows that all carry a value
(#82): the power state, a **command dropdown** (wake, sleep, hibernate, restart,
shut down, lock, screen off, screen on — force shutdown is automation-only) that
runs what you pick and then shows it, a **schedule dropdown** (5/15/30/60/120
minutes, plus *Cancel*, which is `schedule(minutes: 0)`), the schedule summary,
the status summary and the session summary. There is no push button anywhere:
a row without a value renders as "-" on the phone, which is what eight buttons
looked like. Switch on sends Wake-on-LAN (immediately, +2 s, +5 s, ports 7 and
9); switch off sends `offAction` and honours the PC's grace period. A command
picked from the list follows the `buttonMode` preference, exactly as the buttons
did. `pcExec.execute(command, mode, minutes)` is what both the screen and
automations send, and it schedules when `minutes > 0`; the eight no-argument
commands stay in the definition for scenes and for devices still on an older
profile. The raw attributes (`remainingSeconds`, `executeAt`, `origin`,
`serviceVersion`, `updateAvailable`, `wolReady`, `lastSeen`, `idleMinutes`,
`locked`, `user`) are still there for automations, just not on screen. The
separate monitor child device was removed in #81: the screen off/on entries do
the same job on the PC itself, and a child left over from an older driver is
deleted on the driver's first init.

#84: closing the command list without picking anything makes the app send the
row's CURRENT value as the argument, so every value the row can hold has to be
a valid and harmless one. The command row therefore rests on `none` (a real
`execute` argument the driver answers with a refresh and nothing else), what
actually ran is read off the `lastCommand` row below it, and another row picks
which command a schedule runs (`planCommand` / `setPlanCommand`, because a list
carries exactly one argument and the schedule list spends it on the minutes).

#85 fixed two more things measured on the phone. The cloud validates a command's
arguments against the capability definition and never forwards a rejected one,
so the schedule list's Cancel entry (`minutes = 0`) died against `minimum: 1`
with a "system error" popup; the minimum is 0 now, and every list key has to sit
inside its argument's schema. And the app draws a detail row in the card of the
capability that owns it, so `planCommand` / `setPlanCommand` moved onto the
schedule capability, where "what to schedule" now sits above "when".

#86 split the version row onto a capability of its own, `pcVersion`, and made
every row carry a sentence. Two `state` rows of the SAME capability are drawn
side by side in two narrow, truncated columns — that is what the status card's
"상태" and "버전" looked like — while one state row per capability renders full
width. The app also groups all state rows into one card and all dropdowns into
another, so "last card" means "last capability in the profile"; `pcversion` is
last in `pc.v13`, right after `pcinfo`, which keeps defining and emitting
`versions` (dropping it would need another rename, and an attribute that is
never emitted makes the app report incomplete state). An empty string reads as
"-" exactly like an unset attribute, so `lastCommand` says "없음 (None)" / "None"
before anything has run, and a test walks every detail row to keep it that way.
Finally, a command whose bound attribute does not change — cancelling with
nothing scheduled, re-picking the value a list already shows, the `none` no-op —
left the app spinning until it failed, so every emit that answers a command goes
out with `{ state_change = true }`.

The capabilities were renamed `pcControl` -> `pcAction` -> `pcRun` -> `pcExec`
(id `<ns>.pcexec`) and `pcTimer` -> `pcPlan` -> `pcCountdown` (id
`<ns>.pccountdown`) because the hub caches a capability definition by id for the
whole hub and never re-reads a changed one, so a changed definition needs a new
id; the profile goes up a version (`pc.v13`) for the same reason on the
presentation side. Devices are migrated automatically on the driver's first
init, and the driver repaints every attribute of both capabilities once after a
migration — under a new id they all start out unset, which reads as "-".

**Korean** — capability labels, enum values and command arguments are translated
through `capabilities/translations/<name>.{ko,en}.json`, so the app follows the
phone's locale; the string attributes the driver composes (the summaries,
`message`, `lastCommand`) follow the `language` preference instead.
`tools/sync-capabilities.sh` pushes definitions, presentations and both locales.

**Several PCs** — the identity is `machine_id` (Windows MachineGuid), carried at
the top level of SSDP, status and push bodies, so one hub routes several PCs
correctly and a known machine_id updates an address instead of creating a second
device. Secret, MAC and broadcast address are per-device preferences. Polls are
staggered. Cloning a Windows image duplicates the MachineGuid and merges two PCs
into one device — regenerate it on one of them. Telegram stays **one bot per
PC**; sharing a token yields `getUpdates` 409 conflicts.

**Development**

```sh
cd edge
npm install                          # or: bun install
npm test                             # or: bun tools/lua.js tests/run.lua
npm run syntax
./tools/create-capabilities.sh       # once per account: creates the 6 capabilities
node tools/apply-namespace.js <ns>   # writes the assigned namespace everywhere
```

`tools/lua.js` runs the suite under [fengari](https://fengari.io/) (Lua 5.3 in
JavaScript) using only `fs`/`path`, so node and bun both work. No lockfile is
committed — fengari is pinned exactly — so CI uses `npm install`, not `npm ci`.
`.github/workflows/edge.yml` tests every push to develop/main/milestone and every
PR touching `edge/**` and, on an
`edge-vX.Y.Z` tag, checks the tag against `src/driver_version.lua`, packages the driver,
assigns it to the channel and attaches the zip to the release. It needs the
`SMARTTHINGS_TOKEN` and `ST_CHANNEL_ID` repository secrets.

The ids in `src/caps.lua`, `profiles/pc.yml` and `capabilities/*.json` use the
namespace `numbersystem53811` that SmartThings assigned to the owner account (ids are lower-cased by SmartThings, e.g. `numbersystem53811.pcpower`); on another account `apply-namespace.js` rewrites it;
until then `caps.load` skips capabilities it cannot resolve and `switch`,
`refresh` and `healthCheck` keep working.
