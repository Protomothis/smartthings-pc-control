# SmartThings 플랫폼 실측 노트

허브와 휴대폰에서 직접 확인한 플랫폼 동작만 모은 참조 목록이다. 드라이버 설계는
`edge-driver.md`에 있고, 이 문서는 "왜 그렇게밖에 못 만들었는가"의 근거다.
capability·프레젠테이션·프로필을 건드리기 전에 훑어볼 것.

## capability id와 네임스페이스

- 계정에 발급된 네임스페이스는 `numbersystem53811`이다. capability id는 `<네임스페이스>.<이름>`.
- SmartThings는 id의 이름 부분을 **소문자**로 바꾼다(`pcPower` → `numbersystem53811.pcpower`). 정의 JSON의 `name`은 camelCase 그대로다.
- 프레젠테이션 본문의 `id`는 경로의 capability id와 같아야 한다.
- CLI 2.x에는 `login` 명령이 없다. 인증이 필요한 첫 명령에서 브라우저가 열린다.
- `capabilities:update`는 존재하지 않는 id를 거부한다. 새 capability는 `capabilities:create`로만 만든다.

## 허브의 정의 캐시

- 허브는 커스텀 capability 정의를 **id 단위로 허브 전체에** 캐시하고, 같은 id의 정의를 클라우드에서 바꿔도 다시 읽지 않는다.
- 드라이버 재설치, 새 드라이버 id 설치, 장치 삭제·재추가, 프로필 이전 모두 캐시를 비우지 못한다. 확인된 갱신 경로는 허브 재부팅뿐이다.
- 그래서 **정의(속성·명령)를 바꾸려면 새 capability id**를 만들어야 한다. 프레젠테이션·번역만 바꾸는 경우는 해당하지 않는다.
- id가 바뀐 capability는 허브에서 **모든 속성이 값 없이** 시작한다. 옛 id의 값은 따라오지 않으므로 드라이버가 전 속성을 한 번 다시 내보내야 한다.
- 드라이버가 `set_field(..., {persist = true})`로 남긴 "이미 칠했다" 표시는 id 변경을 넘어 살아남는다. 표시에 세대 번호를 붙여야 한 번 더 칠한다(`poll.ROWS_VERSION`).
- capability를 **새로 하나 더 만드는 것**은 개명이 아니다. 기존 정의를 건드리지 않으므로 캐시 문제도, 지울 옛 id도 없다.
- 쓰이지 않게 된 id는 참조가 모두 사라진 뒤 `capabilities:delete`로 계정에서 지운다.
- **배포 후 계정에서 지울 것**: `numbersystem53811.pcdelay`(#91에서 `pcdefer`로 바뀜)와 `numbersystem53811.pcexec`(#93에서 `pcremote`로 바뀜). 드라이버가 배포되고 모든 장치가 `pc.v1`로 이전된 뒤 `smartthings capabilities:delete <id>`.

## 프로필과 화면 생성

- 장치의 **화면 정의는 생성 시점의 capability 프레젠테이션으로 굳는다.** 프레젠테이션을 갱신하고 같은 이름의 프로필을 다시 패키징하면 preference 추가만 반영되고 detailView는 옛 것이 남는다.
- 화면을 다시 만드는 유일한 방법은 장치를 **새 이름의 프로필**로 옮기는 것이다. 그래서 프레젠테이션·번역·capability 목록이 바뀔 때마다 `profiles/pc-vN.yml`(`name: pc.vN`)로 이름 버전을 올린다.
- 옛 프로필 파일은 패키지에 남긴다. 아직 옮겨지지 않은 장치가 참조한다.
- 이전은 `device:try_update_metadata({ profile = "<새 이름>" })`이다. pcall로 감싸고 장치당 드라이버 구동 1회만 시도한다.
- **`try_update_metadata({ model = … })`는 클라우드에 닿지 않는다**(2026-09-26 실측, #94). 허브는 오류 없이 받고 `infoChanged`까지 내지만 클라우드 장치 기록의 `deviceModel`은 생성 시 값(`PC Control`)에 머문다. 모델명·제조사처럼 앱의 장치 정보에 보이는 값은 **생성 시점에 정해진다**고 보고, 사후 갱신은 있으면 좋은 정도로만 둔다.
- `device.profile`은 테이블(`id`, `components`)이지만 `name`이 **항상 있지는 않다.** 생성 시점에 `device:set_field("profile_name", …, {persist = true})`로 저장해 두고 그것을 폴백으로 쓴다.
- preference `title`의 길이 제한(36자)은 `edge:drivers:package`에서만 드러난다.

## 상세 화면(detailView) 위젯

- **`pushButton` 줄은 값이 없어 라벨 옆에 "-"가 남는다.** detailView에는 쓰지 않는다. 값이 있는 `list`로 대체한다.
- detailView의 `list`는 `{"command": {"name": …, "alternatives": […]}, "state": {"value": "<attr>.value", "alternatives": […]}}` 형식이다. `state`를 넣으면 `state.alternatives`가 **필수**다.
- 두 목록의 `key` 집합은 서로 달라도 된다(명령 쪽은 인자 enum, 상태 쪽은 속성 enum).
- **`list.state`가 bool 속성이면 목록이 아예 그려지지 않는다.** 라벨 옆이 "-"이고 꺾쇠도 없어 눌러도 열리지 않는다. `state`는 반드시 문자열 enum 속성이어야 한다.
- `"argumentType": "integer"`는 **고른 키에만** 적용된다. 사용자가 목록에서 값을 고르면 키가 정수로 변환돼 나가지만, 목록을 그냥 닫을 때 나가는 현재 값은 이 변환을 거치지 않는다.
- **그래서 `list`가 보내는 인자는 정수가 아니라 문자열 enum으로 정의해야 한다**(2026-09-26 실측, #91). 정의가 `minutes: integer`인 채로 목록을 고르지 않고 닫으면 문자열이 그대로 나가 클라우드가 422로 막는다 — 앱에는 "네트워크 또는 서버 오류" 팝업만 뜬다.

  ```
  schedule(-1)   → Command executed successfully (허브 수신)
  schedule("-1") → 422 commands[0].arguments[0]: string found, integer expected
  ```

  인자를 키 문자열 enum(`"-1"` `"0"` `"5"`…`"4320"`)으로 정의하고 프레젠테이션에서 `argumentType`을 빼면 두 경로가 같은 값을 보낸다. 숫자가 필요하면 드라이버가 `tonumber`로 되읽는다.
- **인자 검증은 클라우드가 정의를 보고 한다.** `alternatives[].key`가 인자 스키마(enum 집합, `minimum`..`maximum`)를 벗어나면 명령이 **허브에 닿지도 못하고** "시스템 오류" 팝업만 뜬다 — 드라이버 로그에는 아무것도 남지 않는다.
- **목록을 고르지 않고 닫으면 그 줄의 현재 state 값이 그대로 명령 인자로 나간다.** 그래서 ⑴ `state.alternatives`의 키 집합은 명령 첫 인자 enum의 부분집합이어야 하고, ⑵ 줄이 쉬는 값은 무해한 무동작이어야 한다.
- **숫자를 고르는 list 의 state 도 인자가 받아들이는 값이어야 함(`"-1"` 무동작 관례)** — 닫을 때 나가는 현재 값에는 예외가 없다. `status`("idle")에 묶인 예약 시간 목록은 `schedule(minutes: "idle")`을 보내 "네트워크 오류"로 끝났다. 줄이 쉬는 값 하나만 가지는 enum 속성(`minutesPick` = `"-1"`)에 묶고, 그 값을 인자 enum에도 넣는다.
- **강제 이벤트를 연발하면 그 뒤의 이벤트가 사라진다** (2026-09-26 실측, #93 후속). 같은 값의 `state_change` 이벤트 넷이 5초 안에 나간 뒤(17:22:02, :04, :04, :07) 값이 **바뀐** 다음 이벤트가 강제 없이 나가자(17:22:09 `none`) 허브와 클라우드 사이 어딘가에서 사라졌다. 장치 레코드는 90초 동안 옛 값에 머물렀고, 무관한 전환이 같은 속성을 다시 내보내고 나서야 맞춰졌다. 같은 전환을 재전송 없이 되풀이한 두 번째 시도(17:24:52 → 17:25:06)는 멀쩡히 닿았다.
  - 그래서 ⑴ **같은 값을 주기적으로 다시 내보내지 않는다.** 허브가 속성을 들고 있으므로 "계속 그 값이라고 말해 주기" 위한 재전송은 필요가 없고, 그 연발이 바로 사고의 원인이다. 강제 재전송은 앱이 기다리고 있는 **명령의 응답**에만 쓴다.
  - ⑵ **값이 바뀔 때도 강제한다.** `state_change`는 "같아 보여도 전달하라"는 뜻이라 바뀐 값에 붙여도 손해가 없고, 반드시 닿아야 하는 이벤트에 붙일 수 있는 유일한 표시다.
  - ⑶ **한 번 더 보낸다.** 드라이버가 "이미 그 값으로 쉬고 있다"고 판단하면 다시 시도하지 않으므로, 한 번 잃으면 다음 전환까지 줄이 굳는다. 값이 바뀐 다음 폴링에서 같은 값을 한 번만 더 강제로 내보내 이 막다른 길을 없앤다.
- **값이 바뀌지 않는 명령은 회전 표시 뒤 오류로 끝난다.** 앱은 명령을 보낸 뒤 그 줄이 묶인 속성의 이벤트를 기다리는데, 값이 같으면 플랫폼이 이벤트를 버린다. 명령의 응답으로 나가는 emit은 `device:emit_event(cap.attr(value, { state_change = true }))`로 강제한다. 폴링이 스스로 내는 갱신은 강제하지 않는다.
- **한 번도 emit 되지 않은 속성은 "-"이고, 값이 빈 문자열인 줄도 "-"다.** 앱이 "상태를 모두 보고하지 않았다"고 안내한다. 모든 `state` 줄은 해당 사항이 없을 때도 문구를 가져야 한다.
- **`visibleCondition`은 무시된다.** API는 받아 주지만 휴대폰이 반영하지 않는다. 모든 줄은 해당 사항이 없을 때도 혼자 읽혀야 한다.

## supportedValues (#93, 실측 대기)

- **줄을 비활성화하거나 로딩 상태로 두는 방법은 없다.** detailView 위젯에는 `disabled`·`loading`에 해당하는 것이 아예 없다. `pushButton`을 회색으로 만들 수도, 표준 `switch` 토글을 잠글 수도 없다. 그래서 "지금은 누르지 마세요"는 ⑴ 줄이 쉬는 **값**의 문구로 말하고(`lastAction` = `busyOff` → "종료 진행 중…"), ⑵ 드라이버가 실제로 명령을 받아 주지 않는 것으로 구현한다. 화면은 안내일 뿐이고 규칙을 강제하는 것은 드라이버다.
- 그래서 **"언제까지 막을 것인가"는 화면이 아니라 서비스에 물어야 한다.** 이 드라이버는 status의 `grace.seconds`를 그대로 상한으로 쓴다(§6.9). 드라이버가 고른 고정 시간으로 화면 상태를 흉내 내면, 설정을 바꾼 사용자에게만 틀린 화면이 남는다 — 플랫폼이 알려 주지 않는 것을 상수로 메우는 대신 데이터 출처를 찾는 쪽이다.
- detailView `list`의 `command`에 **`"supportedValues": "<attr>.value"`** 를 넣으면 그 속성(문자열 배열)에 담긴 키만 메뉴에 남는다 — 는 것이 이 실험의 가설이다. **아직 실기로 확인하지 않았다.** 확인할 것:
  - 전환 중 `supportedCommands`에 `["busyOff"]`(메뉴에 없는 키 하나)만 실었을 때 목록이 비는가, 아니면 `alternatives` 전체가 그대로 보이는가.
  - 속성이 바뀌면 화면이 **다시 그려지는가**, 아니면 장치를 다시 열어야 하는가.
  - `supportedValues`가 붙은 줄에서 목록을 고르지 않고 닫을 때 나가는 값이 달라지는가(현재 가정: 달라지지 않는다 — 그래서 `busy*`도 `execute`의 인자다).
- **빈 배열은 쓰지 않는다.** 커뮤니티 보고로는 `[]`이면 앱이 제한이 없는 것으로 보고 전체 목록으로 되돌아간다. 그래서 전환 중에도 값 하나(지금 쉬는 `busy*`)는 싣는다.
- 먹지 않으면 이 항목만 문서에 남기고 ⑴·⑵로 간다. `supportedCommands` 속성 자체는 정의에 남겨 둔다 — 빼는 것도 정의 변경이라 또 한 번의 개명이다.
- `automation.actions`에는 붙이지 않는다. 루틴은 한 번 쓰고 나중에 도는 것이라, 지금 PC가 무엇을 하고 있는지로 선택지를 줄이는 것은 먹더라도 틀린 동작이다.

## 화면 배치

- **앱은 상세 줄을 그 줄을 소유한 capability의 카드에 그린다.** 줄의 배치를 바꾸려면 속성·명령의 소속을 옮겨야 하고, 그것은 정의 변경이므로 새 id다.
- **앱은 `state` 줄을 한 카드에, 조작 줄(`list`)을 다른 카드에 모은다.** 상태 카드가 위, 조작 카드가 아래다. 각 묶음 **안에서의** 순서만 프로필의 capability 목록 순서를 따른다.
- **같은 capability의 `state` 줄이 둘이면 두 칸으로 나란히 그려지고 둘 다 "…"로 잘린다.** `state` 줄이 하나뿐인 capability만 전체 폭으로 그려진다. 화면에 보여야 할 상태 줄은 capability당 하나로 둔다.
- 한 줄에 위젯 둘을 나란히 놓는 배치는 없다. 긴 라벨과 긴 요약 문자열은 잘린다고 보고 짧게 쓴다.

## automation 프레젠테이션

- `automation.actions`에는 `multiArgCommand`가 **허용**된다(문서에는 없다). detailView에는 불가하다.
- `automation.actions`에 `pushButton`은 불가하다. 자동화에서 예약을 취소하려면 `schedule(minutes: 0)`을 쓴다.
- `multiArgCommand`의 각 인자 위젯(`list`/`numberField`)에는 인자 이름 `name`이 필수다.

## 번역

- 번역 본문은 `{tag, label, attributes{<attr>{label, i18n{value{<enum>{label}}}}}, commands{<cmd>{label, arguments{<arg>{label}}}}}`이다.
- **앱은 속성 값 라벨에 번역을 적용하지 않는다.** 한국어 로케일에서도 `powerState`는 "On"으로 보였다. 로케일을 타는 것은 capability 라벨과 속성·명령 **라벨**뿐이다.
- 그래서 사용자가 읽는 **값** 문구는 프레젠테이션의 `alternatives[].value`에 직접 쓰고, "한국어 (English)" 병기 형식을 따른다.
- **명령 인자의 enum 값은 번역할 수 없다.** `arguments.<arg>.i18n` 계열은 어떤 형식이든 422다. 인자 라벨만 번역된다.
- 정의를 갱신한 직후의 번역 upsert는 전파 지연으로 "속성 없음" 거부가 날 수 있다. 몇 초 뒤 같은 요청을 다시 보내면 통과한다.

## Edge 런타임

- `config.yml`의 `permissions`에 `discovery`가 없으면, 장치가 하나도 없는 드라이버에서 [주변 기기 검색]이 드라이버를 아예 호출하지 않는다.
- `version`이라는 이름의 모듈은 런타임 모듈을 가린다. 드라이버 버전은 `driver_version.lua`에 둔다.
- LAN 장치 메타데이터의 프로필 키는 `profile`이다.
- cosock 소켓은 `reuseaddr` 옵션을 거부한다("unknown variant"). `timeout`·`keepalive`·`tcp-nodelay`만 받는다.
- `EDGE_CHILD` 장치는 DNI를 지정할 수 없다.
- 장치 health를 offline으로 두면 앱이 스위치를 회색으로 만들어 **Wake-on-LAN을 쓸 수 없다.** PC가 꺼진 것은 health가 아니라 `powerState`·`switch`로 표현하고 health는 online으로 유지한다.
