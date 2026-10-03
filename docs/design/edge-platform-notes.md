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
- **새 capability는 만든 뒤 몇 분 동안 드라이버 패키징이 거절한다**: `edge:drivers:package`가 `Invalid device profile specification`(400)으로 실패하고, 그동안 `capabilities <id>` 조회도 403이다. 2026-10-02 `pcwatch`는 약 6분 뒤에 통과했다. 만든 직후에는 기다렸다가 다시 패키징한다(`pcwatchlist`도 같다).
- **속성·명령 이름은 영문자만**(camelCase, 1–36자, 패턴 `^[[a-z]*([A-Z][a-z]*)*]{1,36}# SmartThings 플랫폼 실측 노트

허브와 휴대폰에서 직접 확인한 플랫폼 동작만 모은 참조 목록이다. 드라이버 설계는
`edge-driver.md`에 있고, 이 문서는 "왜 그렇게밖에 못 만들었는가"의 근거다.
capability·프레젠테이션·프로필을 건드리기 전에 훑어볼 것.

## capability id와 네임스페이스

- 계정에 발급된 네임스페이스는 `numbersystem53811`이다. capability id는 `<네임스페이스>.<이름>`.
- SmartThings는 id의 이름 부분을 **소문자**로 바꾼다(`pcPower` → `numbersystem53811.pcpower`). 정의 JSON의 `name`은 camelCase 그대로다.
- 프레젠테이션 본문의 `id`는 경로의 capability id와 같아야 한다.
- CLI 2.x에는 `login` 명령이 없다. 인증이 필요한 첫 명령에서 브라우저가 열린다.
). 숫자가 들어가면 `capabilities:create`가 `PatternError`로 거절한다(2026-10-02, `slot1`…`slot5` → `slotOne`…`slotFive`).

## 허브의 정의 캐시

- 허브는 커스텀 capability 정의를 **id 단위로 허브 전체에** 캐시하고, 같은 id의 정의를 클라우드에서 바꿔도 다시 읽지 않는다.
- 드라이버 재설치, 새 드라이버 id 설치, 장치 삭제·재추가, 프로필 이전 모두 캐시를 비우지 못한다. 확인된 갱신 경로는 허브 재부팅뿐이다.
- 그래서 **정의(속성·명령)를 바꾸려면 새 capability id**를 만들어야 한다. 허브 캐시와는 별개로 **프레젠테이션도 처음 쓰인 내용으로 굳는다**(아래 "프로필과 화면 생성", 2026-10-02) — 장치 화면에 보이는 것을 바꾸려면 프레젠테이션만 바꾸는 경우에도 새 id가 필요하다. 번역(라벨)만은 해당하지 않는다고 본다(확인 전).
- id가 바뀐 capability는 허브에서 **모든 속성이 값 없이** 시작한다. 옛 id의 값은 따라오지 않으므로 드라이버가 전 속성을 한 번 다시 내보내야 한다.
- 드라이버가 `set_field(..., {persist = true})`로 남긴 "이미 칠했다" 표시는 id 변경을 넘어 살아남는다. 표시에 세대 번호를 붙여야 한 번 더 칠한다(`poll.ROWS_VERSION`).
- capability를 **새로 하나 더 만드는 것**은 개명이 아니다. 기존 정의를 건드리지 않으므로 캐시 문제도, 지울 옛 id도 없다.
- 쓰이지 않게 된 id는 참조가 모두 사라진 뒤 `capabilities:delete`로 계정에서 지운다.
- **배포 후 계정에서 지울 것**: `numbersystem53811.pcdelay`(#91에서 `pcdefer`로 바뀜)와 `numbersystem53811.pcexec`(#93에서 `pcremote`로 바뀜). 드라이버가 배포되고 모든 장치가 `pc.v1`로 이전된 뒤 `smartthings capabilities:delete <id>`. `numbersystem53811.pcmessage`(Dev 채널의 `pc*.v3`)와 `numbersystem53811.pcnotify`(Dev 채널의 `pc*.v4`)는 개발 장치가 `pc*.v5`로 옮겨진 뒤 2026-10-01에 지웠다. `numbersystem53811.pcactivity`(kind 방식 앱 감지, Dev 채널의 `pc*.v5`, #123에서 `pcapps` + 자식 장치의 `pcapp`로 바뀜)와 `numbersystem53811.pcapps`·`numbersystem53811.pcapp`(Dev 채널의 `pc*.v6`과 `pc-app.v1`, 2026-10-02 감시 목록 카드 `pcwatch`로 바뀜, 아래 "자식 장치 대신 슬롯"), 그리고 `numbersystem53811.pcwatch`(Dev 채널의 `pc*.v7`–`v9`, 화면이 첫 프레젠테이션으로 굳어 2026-10-02 `pcwatchlist`로 바뀜, 아래 "프로필과 화면 생성")는 개발 장치가 `pc*.v10`으로 옮겨지고 앱 자식 장치가 지워진 뒤 2026-10-02에 지웠다. 삭제에는 `--capability-version 1`이 필요하다(없으면 CLI가 크래시).

## 프로필과 화면 생성

- 장치의 **화면 정의는 생성 시점의 capability 프레젠테이션으로 굳는다.** 프레젠테이션을 갱신하고 같은 이름의 프로필을 다시 패키징하면 preference 추가만 반영되고 detailView는 옛 것이 남는다.
- 화면을 다시 만드는 유일한 방법은 장치를 **새 이름의 프로필**로 옮기는 것이다. 그래서 프레젠테이션·번역·capability 목록이 바뀔 때마다 `profiles/pc-vN.yml`(`name: pc.vN`)로 이름 버전을 올린다. 커스텀 capability의 프레젠테이션이 바뀌는 경우는 이름만으로 부족하고 새 capability id도 필요하다(아래).
- 옛 프로필 파일은 패키지에 남긴다. 아직 옮겨지지 않은 장치가 참조한다.
- **capability 프레젠테이션도 처음 쓰인 내용으로 굳는다**(2026-10-02 실측, #123): `presentation:update` 뒤 새 프로필 이름(v8, v9)을 내도 장치 화면은 옛 프레젠테이션으로 만들어졌다. 화면을 바꾸려면 새 capability id가 필요하다(pcwatch → pcwatchlist, 2026-10-02).
  - 경과: 감시 목록 카드의 줄 순서와 값 문구를 바꾸려고 `numbersystem53811.pcwatch`(버전 1)에 `capabilities:presentation:update`를 하고 `pc*.v8`을 냈다. `capabilities:presentation` 조회는 새 내용(요약 → 감시 1–5 → 이름, 짧은 한국어 값)을 돌려줬지만 v8 장치의 화면은 옛 것(이름이 둘째 줄, "실행 중 (Running)" 병기 값)이었다. 갱신 직후 패키징 탓으로 보고 10분 뒤 같은 프로필을 새 이름 `pc*.v9`로 냈는데, v9도 옛 프레젠테이션이었다.
  - 그래서 "갱신하고 기다린 다음 새 프로필 이름"으로는 부족하다. 정의가 그대로여도 **화면이 달라지는 프레젠테이션 변경은 새 id**로 낸다: 감시 목록 카드는 `pc*.v10`부터 `numbersystem53811.pcwatchlist`(`pcWatchList`, 정의는 `pcWatch`와 같다)다. 새 id에는 처음부터 지금의 프레젠테이션으로 `capabilities:presentation:create`를 한다. 프로필 이름도 여전히 올린다(capability 목록이 바뀐다).
  - `presentation:update`는 조회 결과와 기록용으로만 의미가 있다고 본다. 이미 쓰인 id의 프레젠테이션을 고쳐서 화면을 바꾸려 하지 않는다.
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
- **프로필 이전 뒤에는 같은 값이 영영 안 올라갈 수 있다**(1.1.0, 2026-09-30 실측). v1→v2 이전 뒤 허브는 `audioMute.mute`를 이미 "unmuted"로 알고 있어 강제 아닌 "unmuted"를 모두 버렸고, 클라우드는 한 번도 저장하지 못해 null이었다(앱의 "상태 정보를 모두 받지 못함" 경고). 강제 전송 한 번으로 바로 저장됐다. 그래서 드라이버는 실행마다 **각 줄의 첫 전송을 강제**한다(`fields.ROWS_FORCED`). 또 서비스가 값을 주지 않는 표준 속성(`audioTrackData.totalTime`/`elapsedTime`)도 비워 두면 null이 되므로 0으로 채운다.
  - 그래서 ⑴ **같은 값을 주기적으로 다시 내보내지 않는다.** 허브가 속성을 들고 있으므로 "계속 그 값이라고 말해 주기" 위한 재전송은 필요가 없고, 그 연발이 바로 사고의 원인이다. 강제 재전송은 앱이 기다리고 있는 **명령의 응답**에만 쓴다.
  - ⑵ **값이 바뀔 때도 강제한다.** `state_change`는 "같아 보여도 전달하라"는 뜻이라 바뀐 값에 붙여도 손해가 없고, 반드시 닿아야 하는 이벤트에 붙일 수 있는 유일한 표시다.
  - ⑶ **한 번 더 보낸다.** 드라이버가 "이미 그 값으로 쉬고 있다"고 판단하면 다시 시도하지 않으므로, 한 번 잃으면 다음 전환까지 줄이 굳는다. 값이 바뀐 다음 폴링에서 같은 값을 한 번만 더 강제로 내보내 이 막다른 길을 없앤다.
- **값이 바뀌지 않는 명령은 회전 표시 뒤 오류로 끝난다.** 앱은 명령을 보낸 뒤 그 줄이 묶인 속성의 이벤트를 기다리는데, 값이 같으면 플랫폼이 이벤트를 버린다. 명령의 응답으로 나가는 emit은 `device:emit_event(cap.attr(value, { state_change = true }))`로 강제한다. 폴링이 스스로 내는 갱신은 강제하지 않는다. 줄이 아무 속성에도 묶여 있지 않으면 기다릴 이벤트 자체가 없어 늘 이렇게 끝난다(아래 "표준 capability", `pcNotify`).
- **한 번도 emit 되지 않은 속성은 "-"이고, 값이 빈 문자열인 줄도 "-"다.** 앱이 "상태를 모두 보고하지 않았다"고 안내한다. 모든 `state` 줄은 해당 사항이 없을 때도 문구를 가져야 한다.
- **`visibleCondition`은 무시된다.** API는 받아 주지만 휴대폰이 반영하지 않는다. 모든 줄은 해당 사항이 없을 때도 혼자 읽혀야 한다.

## supportedValues (#93, 미확인)

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
- **컴포넌트 카드는 장치 메인 화면에서 미리보기로 그려진다**(2026-10-02 휴대폰 실측, #123). 컴포넌트 `apps`("감시 목록")는 메인 화면에 카드 하나로 나오고, 그 미리보기는 detailView의 **처음 상태 줄 세 개**를 가로로 나란히, 각각 폭의 약 1/3에 큰 글씨로 보여 준다. 값이 한 줄을 넘으면 둘째 줄로 감기고 카드가 잘라 낸다 — v7의 "Claude 실행 중", "1 Claude", "실행 중 (Running)"이 모두 그랬다. 그래서 컴포넌트 카드의 **처음 세 줄은 짧은 한 줄 값**(이름 하나나 한 단어, 열두 자 안팎)으로 두고, 긴 목록 문자열은 뒤로 보낸다. 나머지 줄은 카드를 열었을 때의 상세에 나온다. `pc*.v10`(`pcWatchList`)은 요약 → 감시 1–5 → 이름 순서라 미리보기가 "실행 중인 앱 · 감시 1 · 감시 2"이고, 요약은 "Claude" / "Claude 외 1"(앱 이름은 13자까지, 넘으면 12자 + "…"), 슬롯 값은 "실행 중" / "꺼짐" / "비어 있음"이다.

## automation 프레젠테이션

- `automation.actions`에는 `multiArgCommand`가 **허용**된다(문서에는 없다). detailView에는 불가하다.
- `automation.actions`에 `pushButton`은 불가하다. 자동화에서 예약을 취소하려면 `schedule(minutes: 0)`을 쓴다.
- `multiArgCommand`의 각 인자 위젯(`list`/`numberField`)에는 인자 이름 `name`이 필수다.

## 표준 capability (#108, 2026-09-30 계정에서 확인)

- **`notification`**(status `live`): 명령 `deviceNotification(notification: string, maxLength 255)`, 속성 없음. 프레젠테이션에 detailView `textField`(라벨 "텍스트 표시")와 `automation.actions`의 `textField`가 **둘 다** 있다 — 루틴 동작으로도, 장치 화면의 입력 줄로도 나온다.
- **`speechSynthesis`**(status `proposed`): 명령 `speak(phrase: string, maxLength 1000)`, 속성 없음. 프레젠테이션은 detailView `textField` + `automation.actions` `textField`. 소리내어 읽기를 빼면서(2026-10-01) 쓰지 않기로 해 루틴 동작 목록에 나오는지는 확인하지 않았다.
- **표준 capability의 라벨은 장치 쪽에서 바꿀 수 없다**(2026-09-30, 문서 확인). 휴대폰은 표준 capability의 줄·루틴 동작 이름을 삼성의 번역으로 쓴다 — `notification`은 "텍스트 표시", `speechSynthesis`는 "음성 합성". 프로필에도, 임베디드 장치 구성(device configuration)에도 표준 capability의 라벨이나 i18n을 덮어쓰는 자리가 없다. 우리가 문구를 정할 수 있는 것은 번역 파일을 올리는 자기 네임스페이스의 capability뿐이다.
- 그래서 문구가 중요한 줄은 표준을 쓸 수 없다. "PC에 메시지 보내기"는 커스텀 `pcToast`(`send(text)` + 속성 `lastMessage`)로 옮겼다(media-notify.md §5). 소리내어 읽기(`speak`)까지 있던 `pcMessage`와 속성 없는 `pcNotify`는 Dev 채널에서만 쓰였고, 정의가 바뀔 때마다 새 id가 됐다. 모양은 표준 `notification`을 따른다: detailView와 `automation.actions`에 같은 `textField`(`{"command": …, "argumentType": "string", "range": [1, 200]}`), 라벨은 `{{i18n.commands.<cmd>.label}}`. detailView 쪽에만 `"value": "lastMessage.value"`를 더한다.
- **속성 없는 커스텀 capability를 detailView 줄로 쓰면 앱이 기다릴 이벤트가 없어 회전 표시 뒤 네트워크 오류로 끝난다**(2026-10-01 실측, `pcNotify`). 정의는 받아들여지고 명령도 허브에 닿아 PC에 토스트가 떴지만, 입력 줄은 돌다가 "네트워크 오류"를 띄웠다. **입력 줄은 반드시 속성에 묶는다.** `pcToast`는 문자열 속성 `lastMessage`에 묶고, 드라이버가 `send`마다 — 보냈든 거절했든 — 그 속성을 `state_change = true`로 내보낸다(보냈으면 보낸 문구, 아니면 지금 값). 쉬는 값은 "없음"/"None"이고 빈 문자열은 쓰지 않는다. 루틴 동작(`automation.actions`)은 보여 줄 값이 없으므로 명령만으로 된다. 같은 날 Dev 허브에서 `pcToast` 줄이 회전 표시 없이 끝나고 보낸 문구를 보여 주는 것을 확인했다.
- 표준은 문구가 앱의 것이어도 괜찮은 곳(스위치, 볼륨, 미디어 버튼, 배터리)에만 쓴다.
- `proposed` 표준 capability는 허브에서 `st.capabilities[<id>]`가 풀리지 않을 수 있다고 보고 등록을 pcall로 감싸야 한다. (v2 장치용으로 남겼던 `notification`·`speechSynthesis` 핸들러는 v2가 공개되지 않아 지웠다.)

## 번역

- 번역 본문은 `{tag, label, attributes{<attr>{label, i18n{value{<enum>{label}}}}}, commands{<cmd>{label, arguments{<arg>{label}}}}}`이다.
- **앱은 속성 값 라벨에 번역을 적용하지 않는다.** 한국어 로케일에서도 `powerState`는 "On"으로 보였다. 로케일을 타는 것은 capability 라벨과 속성·명령 **라벨**뿐이다.
- 그래서 사용자가 읽는 **값** 문구는 프레젠테이션의 `alternatives[].value`에 직접 쓰고, "한국어 (English)" 병기 형식을 따른다.
- 예외: 감시 목록 카드의 슬롯 값(상세 줄)은 미리보기 칸이 좁아(위 "화면 배치") 한국어 한 단어 "실행 중" / "꺼짐" / "비어 있음"이다(`pc*.v10`, `pcWatchList`). 영어 `Running` / `Stopped` / `Empty`는 번역 파일의 `i18n.value`에 남겨 두었다 — 위 관찰대로라면 영어 로케일에서도 한국어로 보이고, 앱이 enum 값을 번역에서 가져온다면 영어로 보인다. 어느 쪽인지 기기에서 확인한다(맨 아래 "남은 실측"). 루틴 조건의 값은 병기 그대로다.
- **명령 인자의 enum 값은 번역할 수 없다.** `arguments.<arg>.i18n` 계열은 어떤 형식이든 422다. 인자 라벨만 번역된다.
- 정의를 갱신한 직후의 번역 upsert는 전파 지연으로 "속성 없음" 거부가 날 수 있다. 몇 초 뒤 같은 요청을 다시 보내면 통과한다.

## Edge 런타임

- `config.yml`의 `permissions`에 `discovery`가 없으면, 장치가 하나도 없는 드라이버에서 [주변 기기 검색]이 드라이버를 아예 호출하지 않는다.
- `version`이라는 이름의 모듈은 런타임 모듈을 가린다. 드라이버 버전은 `driver_version.lua`에 둔다.
- LAN 장치 메타데이터의 프로필 키는 `profile`이다.
- cosock 소켓은 `reuseaddr` 옵션을 거부한다("unknown variant"). `timeout`·`keepalive`·`tcp-nodelay`만 받는다.
- `EDGE_CHILD` 장치는 DNI를 지정할 수 없다.
- 하위 폴더의 모듈은 점 이름(`require "device.emit"`, `src/`가 뿌리)으로 허브에서 풀린다(2026-10-01, #129).

## 자식 장치 (EDGE_CHILD, #123)

허브에서 직접 확인한 것과 lua_libs(api v13의 `st/driver.lua`·`st/device.lua` 소스)에서 읽은 것을 나눠 적는다. 감시 앱마다 자식 장치를 두던 `pc*.v6`(Dev 채널)이 이 위에 서 있었고, 2026-10-02 PC 장치의 슬롯 카드로 바뀌었다(아래 "자식 장치 대신 슬롯"). 지금 드라이버는 자식을 만들지 않고, 남은 자식을 지울 때만 이 사실들을 쓴다(설계 §4.2).

- **허브에서 확인**(2026-09-22, #73의 모니터 자식): LAN 드라이버가 `driver:try_create_device{ type = "EDGE_CHILD", parent_device_id, parent_assigned_child_key, … }`로 자식을 만들 수 있다. `device_network_id`를 주면 허브가 경고하고 버린다 — 자식은 `parent_assigned_child_key`로 알아본다.
- **소스에서 읽음**:
  - `try_create_device`의 메타데이터 값은 **전부 문자열**이어야 하고(아니면 `error`), `type`·`label`·`profile`이 필수, EDGE_CHILD는 `parent_device_id`가 필수다. `vendor_provided_label`은 LAN에만 실린다(EDGE_CHILD에는 무시). 생성은 비동기다 — 장치는 나중에 `added` lifecycle로 온다.
  - `driver:try_delete_device(device_uuid)`가 LAN·EDGE_CHILD 장치를 지운다(2026-10-02 Dev 허브에서 v6 앱 자식 장치가 실제로 지워짐을 확인). 지원하지 않는 허브에서는 `nil, "hub does not support device delete functionality"`를 돌려준다. `device:try_delete_device`라는 메서드는 없다(#81의 `remove_legacy_child`가 둘 다 시도하는 이유).
  - 자식 장치 객체에는 `parent_device_id`와 `parent_assigned_child_key`가 있다. `device:get_child_list()`·`get_child_by_parent_assigned_key(key)`는 `driver:get_devices()`를 훑는다.
  - `device:get_parent_device()`는 부모의 장치 정보를 막히는 호출로 가져올 수 있어 **`init`·`added` 안에서 쓰지 말라**고 적혀 있다. 그래서 드라이버는 이번 구동에서 본 부모를 메모리에 기억해 쓴다.
  - `try_update_metadata`가 바꿀 수 있는 것은 `profile`·`provisioning_state`(LAN은 `manufacturer`·`model`·`vendor_provided_label`도)뿐이다. **라벨은 생성 뒤 드라이버가 바꿀 수 없다.**
  - 자식의 lifecycle(`init`·`added`·`removed`·`infoChanged`·`doConfigure`)과 capability 명령은 부모와 같은 핸들러로 온다. 드라이버가 `parent_assigned_child_key`로 갈라야 한다.
- **허브에서 확인**(2026-10-01, #123): 감시 목록의 앱마다 `pc-app.v1` 자식이 생기고 `pcApp.running`이 클라우드에 닿는다. 지우기는 맨 아래 "남은 실측".

## 자식 장치 대신 슬롯 (#123, 2026-10-02)

앱마다 자식 장치를 두는 방식은 허브에서 동작했지만(위) 사용자 결정으로 버렸다. 이유는 플랫폼 쪽에 있다.

- **루틴에서 찾는 곳이 PC 장치다.** 루틴의 조건을 고를 때 사용자는 "PC" 장치 밑을 본다. 자식 장치는 방·장치 목록에 따로 흩어진 별개 장치라, "Steam이 실행 중이면"을 만들려면 그 장치가 있다는 것부터 알아야 했다. 슬롯은 PC 장치의 조건 목록에 "감시 1"–"감시 5"로 바로 나온다.
- **동적인 이름은 조건 라벨이 될 수 없다.** 조건 목록의 라벨은 프레젠테이션(`{{i18n.attributes.<attr>.label}}`)과 번역 파일에서 온다. 둘 다 계정에 올리는 정적 문서이고 장치마다·시점마다 바꿀 수 없다(위 "번역", "프로필과 화면 생성"). 그래서 "Steam"을 조건 이름으로 내보이려면 앱마다 장치(라벨 = 앱 이름)가 필요했고, 그 라벨조차 생성 뒤에는 드라이버가 바꿀 수 없다. 슬롯 번호는 정적이므로 라벨이 될 수 있고, 어느 앱인지는 같은 카드의 이름 줄("1 Steam · 3 OBS")과 PC 앱의 번호가 말한다 — 프리셋(`pcPreset`)이 이미 쓰는 방식이다.
- 그래서 정의는 슬롯마다 속성 하나(`slotOne`–`slotFive`, enum `running`/`stopped`/`empty`)다. 조건에는 `running`/`stopped`만 둔다 — `empty`는 기다릴 사건이 아니라 쉬는 값이고, 빈 줄은 "-"가 되므로 값은 언제나 있다(위 "상세 화면(detailView) 위젯").
- 카드는 PC 장치의 컴포넌트 `apps`(label "감시 목록")다. 한 capability의 상태 줄이 일곱이라 main에 두면 반 폭 두 칸으로 잘리고 다른 상태 줄과 섞인다(위 "화면 배치"). 컴포넌트 카드는 메인 화면에서 처음 세 상태 줄의 미리보기로 그려진다(위 "화면 배치", 2026-10-02) — 그래서 `pc*.v10`(`pcWatchList`)의 순서는 요약 → 감시 1–5 → 이름이고 값은 한 줄에 드는 길이다.
- 대가: 슬롯은 다섯이고, 슬롯 번호가 곧 우선순위다. 앱을 다른 번호로 옮기면 루틴이 가리키는 앱도 바뀐다.

## 상태 캐시와 infoChanged (#129, 소스에서 읽음)

- 장치 객체의 `state_cache`는 드라이버가 마지막으로 내보낸 값을 컴포넌트·capability·속성별로 담고 **재시작을 넘어 보존된다**(persistent store; 플래시 쓰기는 주기적이라 정전에는 잃을 수 있다). `device:get_latest_state(component, capability, attribute)`로 읽는다. 드라이버는 프로필이 그대로인 재시작의 첫 폴링에서 이것을 "이미 보낸 값"으로 쓴다(설계 §6.1). **이 캐시는 클라우드가 받지 못한 값도 담는다** — 허브가 받은 뒤 예산 때문에 버려진 이벤트의 값(2026-10-01 이전의 `pcApps.summary`, 아래 "이벤트 예산"). 그래서 캐시를 믿고 건너뛴 줄도 순환 재전송이 한 바퀴 안에 다시 보낸다.
- `infoChanged` 핸들러의 `args.old_st_store`는 바뀌기 전 장치 기록이다. 그 `profile`(id·components)을 지금 `device.profile`과 비교하면 프로필이 바뀐 `infoChanged`인지 환경설정·라벨만 바뀐 것인지 가를 수 있다. 실제 허브에서 착지 때 id가 달라지는지는 확인하지 않았다(맨 아래).
- 장치 health를 offline으로 두면 앱이 스위치를 회색으로 만들어 **Wake-on-LAN을 쓸 수 없다.** PC가 꺼진 것은 health가 아니라 `powerState`·`switch`로 표현하고 health는 online으로 유지한다.

## 이벤트 예산(rate limit)

2026-10-01, 허브 logcat과 클라우드 이력을 함께 놓고 실측했다(드라이버 1.1.0 개발판, v1.2.0 서비스).

- **장치마다 이벤트 예산이 있고, 허브가 버리는 이벤트도 예산을 쓴다.** 당시 드라이버는 정기 폴링(약 30초)마다 줄 약 40개(switch, pcPower, pcRemote, pcDefer×6, pcInfo×8, pcUser×5, 미디어×3, audioTrackData×3, audioVolume, audioMute, pcPreset×2, pcActivity×2, 잠들지 않기 스위치, pcToast …)를 다시 내보내고 바뀌지 않은 값은 허브가 버리리라 믿었으며, 명령이 성공할 때마다 같은 전체 폴링을 한 번 더 돌렸다. 음소거/해제 명령 넷을 1.5–2초 간격으로 보내자 8초 동안 `emitting event` 줄이 약 230개 찍혔다.
- **예산을 넘으면 그 장치의 이벤트가 모두 사라진다.** 클라우드에는 첫 묶음만 저장되고, 그 뒤 약 40초 동안 장치의 **모든** 이벤트 — 실제로 바뀐 `pcInfo.lastSeen`도, `state_change = true`로 강제한 `audioMute.mute`도 — 가 버려졌다. 다른 시도에서는 한 묶음 중간에서 끊겼다(묶음의 16번째쯤인 lastSeen은 저장, 30번째쯤인 mute는 버려짐). 위 "강제 이벤트를 연발하면 그 뒤의 이벤트가 사라진다"(2026-09-26)도 같은 현상으로 보인다.
- **잃은 이벤트가 허브 캐시를 오염시킨다.** 허브의 상태 캐시는 버려진 값을 이미 받아들였다. 그래서 뒤이은 정상적인(강제 아닌) `muted` 전송을 허브가 "안 바뀜"으로 버려, 클라우드는 무기한 `unmuted`에 머물렀고 휴대폰은 사용자가 해제하려 할 때마다 `mute`를 보냈다. 값이 다시 바뀌거나 강제 전송이 닿을 때까지 풀리지 않는다.
- 그래서 드라이버(`device/emit.lua`·`poll.lua`)는:
  - ⑴ **스스로 중복을 거른다.** 실행마다 메모리에 줄(`emit.row_key`)별 마지막 전송 값을 정규화해 두고(`fields.ROWS_SENT`), 강제 아닌 같은 값은 `emit_event`를 부르지도 않는다. 강제는 언제나 나가고 기록을 갱신한다. 실행마다 첫 전송 강제(`fields.ROWS_FORCED`)는 그대로이고, 프로필이 바뀌면(`poll.repaint`) 기록을 비운다. 고른 상태의 정기 폴링은 이제 `lastSeen` 하나만 낸다.
  - ⑵ **명령의 응답 폴링을 합친다.** 첫 명령은 바로 폴링하고 1.5초 창(`poll.ANSWER_WINDOW_SECONDS`)을 연다. 창 안에 들어온 명령들은 강제할 줄을 모아 창이 닫힐 때 폴링 한 번으로 답한다. 모든 명령의 줄이 한 창 안에 강제 응답을 받으므로 회전 표시 규칙(위 "값이 바뀌지 않는 명령은 회전 표시 뒤 오류로 끝난다")은 그대로다.
  - ⑶ **잃은 값을 되살린다.** 처음에는 10분마다, 그리고 10초 안에 명령이 셋 이상 오면 60초 뒤 한 번 더, 사용자가 보는 줄 여섯(main switch, `pcPower.powerState`, `audioMute.mute`, `audioVolume.volume`, `mediaPlayback.playbackStatus`, 잠들지 않기 switch)만 마지막으로 보낸 값 그대로 강제로 다시 보냈다. 10분짜리는 아래 2026-10-01 이전 실측 뒤 **모든 줄의 순환 재전송**으로 바뀌었고, 명령 몰아치기 뒤의 여섯 줄은 그대로다. 위 #93 후속의 "같은 값을 주기적으로 다시 내보내지 않는다"가 막는 것은 몇 초 안의 연발이고, 이것은 폴링마다 몇 개다.
  - ⑷ 드라이버가 10초에 20개를 넘게 내면 logcat에 `event budget` 경고를 한 번 남긴다(버리지는 않는다).
- 같은 측정 조건의 테스트(`tests/budget_test.lua`)에서 정기 폴링은 39 → 1개, 음소거 명령 넷은 156 → 12개(1.5초 안에 몰아치면 6개)다.
- #129(W2)에서 남은 묶음도 줄였다: ⑴ 다시 칠하기는 줄마다 강제 한 번이고, 30초·90초 후속은 바뀐 줄 + ⑶의 여섯 줄뿐이다. ⑵ `infoChanged`는 프로필이 바뀌었을 때만 다시 칠한다. ⑶ 프로필이 그대로인 재시작의 첫 폴링은 허브 상태 캐시와 같은 값을 보내지 않는다(위 "상태 캐시"). ⑷ 전원·예약 명령도 응답 창을 함께 쓴다. 테스트 기준: 재시작 첫 폴링 42 → 6, 환경설정만 바뀐 `infoChanged` 69 → 0. 다시 칠하기를 나눠 보내는 규칙은 아래 2026-10-01 항목이다.
- 경고선 20개/10초는 플랫폼의 한도(모름)가 아니라 드라이버가 정한 값이다. 측정에서 잃은 것은 8초에 230개였다.
- **이전의 한 묶음에서도 줄을 잃고, 그 손실은 새로 고침·재시작을 넘어 남는다**(2026-10-01, Dev 허브, 드라이버 2026-10-01T10:06/10:11 배포본). v6 프로필(pcApps가 pcActivity를 대신)을 배포하자 개발 장치가 pc-monitor.v5 → v6으로 옮겨졌고, 그 구동이 약 49개를 한꺼번에 냈다. 클라우드의 `numbersystem53811.pcapps.summary`는 10분 넘게 **null**이었고 `refresh` 명령도, 드라이버 재배포·재시작도 고치지 못했다. 앱 자식 장치의 `pcApp.running`은 제대로 닿았다. 그 뒤 logcat의 정기 폴링은 `pcInfo.lastSeen` 하나만 냈다 — summary는 다시 보내지지 않았다. 해석: summary는 묶음에서 허브 상태 캐시가 받은 **뒤에** 버려졌고(위 예산 손실과 같은 모양), 그러자 ⒜ 드라이버의 중복 거르기(`SENT_FIELD`, 값이 같으니 안 보냄)와 ⒝ 재시작의 캐시 건너뛰기(`get_latest_state`가 같으니 안 보냄, 아래 #129 ⑶)가 둘 다 그 줄을 "보냄"으로 믿었다. 앞서 적어 둔 위험 — "캐시가 클라우드와 어긋나면 여섯 줄 밖의 줄은 값이 바뀔 때까지 틀린 채로 남는다" — 이 첫 실제 이전에서 일어났다.
- 그래서 규칙: **모든 줄은 10분 안에 한 번 강제로 다시 보낸다(순환).** 드라이버(`poll.rotate_due`)는 폴링 주기마다 한 번, 이번 실행에서 보낸 줄 전부(main·컴포넌트, 정렬된 줄 키 순서)에서 다음 K줄을 마지막으로 보낸 값 그대로 `state_change = true`로 보낸다. K = ceil(줄 수 ÷ (600초 ÷ 주기)), 1–5 — 기본 30초 주기·줄 약 42개면 폴링마다 3개, 한 바퀴 약 7분. 이번 폴링이 막 보낸 줄은 건너뛰고, PC가 꺼져 실패한 폴링에서도 돈다. (당시의 앱 자식 장치는 한 단계에 하나씩, 각자 10분에 한 번이었다. 2026-10-02에 자식 장치를 없앴다.) 잃은 이벤트는 허브 캐시가 어떻든 한 바퀴 안에 저절로 낫는다. 재시작의 캐시 건너뛰기는 남긴다 — 틀린 캐시의 대가가 한 바퀴로 묶였고, 없애면 재시작마다 약 40개 묶음이 돌아온다. 그리고 **프로필이 바뀐 뒤의 다시 칠하기도 나눈다**(`emit.paint`): 사용자가 먼저 보는 줄(switch, powerState, `pcInfo.summary`, 앱 요약 — 지금은 `apps` 컴포넌트의 `pcWatchList.summary` —, 음소거·볼륨·재생, 잠들지 않기)을 첫 묶음으로, 나머지를 5초마다 9개씩. 테스트 기준: 이전 구동의 가장 바쁜 10초 48 → 18개, 모든 줄 20초 안(착지 `infoChanged`가 겹쳐도 18개·25초). 대가는 고른 상태의 폴링마다 3개(10분에 26 → 80개)다. 이 규칙은 같은 날 Dev 허브에서 확인했다.

## 연결 거부와 시간 초과 (2026-10-03, 멈춘 서비스도 시간 초과로 실측)

드라이버는 전송 오류 문구로 "PC는 켜져 있고 PC 앱만 응답 없음"(`app_down`)과 "PC 꺼짐·네트워크 끊김"(`unreachable`)을 가른다(설계 §3.1, `client.transport_kind`). 근거는 PC 쪽에 있다.

- (가정 — 아래 2026-10-03 실측에서 성립하지 않았다) 서비스는 설치 때 자기 TCP 명령 포트에 **인바운드 허용 규칙**을 만든다(`service/firewall.go`). 규칙이 있는 포트에 아무도 듣고 있지 않으면 Windows는 SYN을 버리지 않고 RST로 답한다 → 허브의 connect가 즉시 `ECONNREFUSED`. 규칙이 없으면 기본 방화벽은 조용히 버리므로 거부가 아니라 시간 초과가 된다 — 이 구분은 **규칙에 기대고 있다**(사용자가 규칙을 지웠거나 다른 방화벽이 막으면 앱이 멈춘 PC도 `unreachable`로 보이고, 예전처럼 2회 뒤 꺼짐이 된다. 해롭지는 않다).
- 꺼졌거나 잠들었거나 선이 빠진 PC는 ARP에도 답하지 않으므로 시간 초과(또는 `No route to host`·`Host is unreachable`)다. 공유기는 LAN 호스트 대신 RST를 보내지 않는다.
- 오류 문구: luasocket은 `ECONNREFUSED`를 "connection refused"로, `ECONNRESET`과 끊긴 연결을 둘 다 "closed"로, 시간 초과를 "timeout"으로 쓰고, 그 밖은 `strerror`다. 허브의 소켓 계층이 Rust라면 "Connection refused (os error 111)" 꼴일 수 있다. 그래서 대소문자 없이 `refused`가 들어 있는지만 본다. "closed"는 연결 단계인지 알 수 없어 `unreachable`에 둔다.
- **"closed"의 예외(2026-10-03, 미확인)**: 장애가 서비스의 `power.stopping` `app_stop`(서비스만 멈추고 PC는 켜진 채)으로 시작됐으면, 다음 성공·`off`까지 "closed"·"reset"이 든 문구도 `app_down`으로 읽는다(설계 §3.1). 이유: 허브의 소켓 계층이 닫힌 포트의 RST를 "connection refused"가 아니라 "closed"로 적을 수 있고, 방금 "PC는 그대로"라고 말한 PC가 실제로 꺼졌다면 `shutdown`/`restart`를 보냈을 것이다. 그래도 PC가 사라지면 시간 초과가 평소대로 2회에 꺼짐으로 데려간다. 이 예외가 실제로 필요한지(허브가 정말 "closed"라고 하는지)는 아래 로그로 확인한다.
- **전송 오류 로그(2026-10-03)**: 허브가 거부된 연결에 실제로 어떤 문구를 주는지는 아직 아무도 보지 못했다. 그래서 `client.request`가 전송 오류마다 `log.info("transport error: <원문> -> app_down|unreachable (<장치 id>)")`를 남긴다 — 장치마다 원문이나 분류가 직전과 다를 때만(`fields.TRANSPORT_ERROR`). HTTP 응답이 한 번 오면 지워지므로 다음 장애의 첫 오류는 같은 문구라도 다시 찍힌다. 꺼진 PC가 30초마다 줄을 쌓지 않는다.
- 실제로 본 것(2026-10-03, 허브): 서비스를 그냥 멈추자 "PC 꺼짐"이 떴다. 원인은 서비스가 보낸 `power.stopping` `unknown`(→ `shuttingDown` → 2회 실패로 `off`)이었고, 서비스가 `app_stop`을 보내도록 고쳤다(계약 C1). 그때의 허브 오류 문구는 남아 있지 않다.
- **실측(2026-10-03, rc15, 실제 허브): 멈춘 서비스의 포트는 거부가 아니라 침묵이다.** 서비스를 멈춘 PC에 대한 허브의 폴링은 "connection refused"가 아니라 **시간 초과**로 끝났다. 허용 규칙이 있어도 Windows 방화벽의 스텔스 모드가 닫힌 포트로 온 SYN을 RST 없이 버리기 때문으로 본다. 그러니 위의 "규칙이 있으면 RST" 가정은 이 PC에서 성립하지 않고, `refused`는 실제로 나타나지 않는다 — 앱이 멈춘 PC와 꺼진 PC를 허브가 전송 오류로 구별할 수 없다. **"PC 앱 응답 없음"은 서비스가 정지 직전에 보내는 `power.stopping` `app_stop` push(설계 §3.5)에만 기댄다.** 같은 날 그 push가 `context deadline exceeded (1/3)`로 실패해(정지 로그 14:32:10 "Service stopping (stop)" → 14:32:11 실패, 10:44:21에도 같은 실패) 허브가 "PC 꺼짐"을 보였다. 원인은 push 본문의 status가 만료된 WoL 캐시 때문에 PowerShell 스캔(약 1초+)을 먼저 돌아 1.5초 마감을 다 쓴 것이었고, 정지 push 본문을 메모리 상태로만 만들고 `app_stop` 마감을 4초로 늘려 고쳤다(설계 §3.5). 정지 push가 끝내 닿지 못하면(허브가 그 순간 응답하지 않는 등) 그 PC는 예전처럼 2회 시간 초과 뒤 "꺼짐"으로 보인다.
- **실측(2026-10-03 14:55, 실제 허브): push는 닿았지만 다음 폴링이 그것을 지웠다.** 고친 서비스의 `app_stop` push가 도착해 허브 로그에 `PC app on <id> stopped (power.stopping app_stop)`이 찍히고 "PC 앱 응답 없음"이 제대로 떴다. 그런데 바로 다음 폴링이 `transport error: [string "socket"]:98: timeout -> unreachable`로 실패해 앱 중지 표시를 지웠고, 두 번째 시간 초과에서 PC가 "꺼짐"이 됐다. 이 문구가 허브(cosock/luasocket)의 시간 초과 문구다. 멈춘 서비스의 포트는 늘 침묵이므로 `app_stop` 뒤 폴링은 언제나 시간 초과이고 거부는 오지 않는다. 그래서 **`app_stop` 뒤 10분 보류 창**(설계 §6.2, `state.APP_STOP_HOLD`)을 두었다: push를 받은 허브 시각부터 10분 동안 거부가 아닌 전송 오류(시간 초과 포함)를 모두 `app_down`으로 읽고 — 전원 `on`, 요약·메시지는 앱 응답 없음, `off` 쪽으로 세지 않는다 —, 성공한 status·HTTP 응답·다른 `power.stopping`·10분 경과에서 창을 닫는다. 10분이 지나면 평소 규칙이라, 앱이 멈춘 채 꺼진 PC도 결국 "꺼짐"이 된다(창이 끝난 뒤 시간 초과 2회). 창의 시작은 push 로그(`… stopped (power.stopping app_stop): app_stop hold 600s`), 끝은 `app_stop hold on <id> ended (<이유>)` 한 줄씩이고, 창 안의 시간 초과는 위 전송 오류 로그처럼 바뀔 때만 한 줄(`… timeout -> app_down`)이다. push 시각은 영속 필드(`app_stop_at`)에도 적어 창 안의 드라이버 재시작이 창을 이어 간다. 10분은 업데이트·서비스 재시작이 끝나기에 넉넉하고, 앱을 멈춘 채 PC를 끈 경우 "꺼짐"이 늦어지는 상한이다.
- 위험: PC가 다른 주소로 옮기고 옛 주소를 다른 기기가 받아 RST로 답하면 "PC 앱 응답 없음"이 계속된다. 그래서 거부에도 `unreachable`처럼 장치당 5분에 한 번 표적 SSDP 검색을 돈다(설계 §6.5). 다만 PC 앱이 멈춘 PC는 SSDP에도 답하지 않는다(SSDP 응답기는 서비스 안에 있다).
- **실측할 것**: PC 앱(서비스)을 멈춘 PC에 대한 허브 logcat의 `transport error:` 줄 — 문구가 `refused`를 포함하는지, "closed"인지(그러면 `app_stop` 없이 멈춘 앱도 `unreachable`로 보이므로 규칙을 다시 본다), 꺼진 PC는 "timeout"인지. 문구가 다르면 `client.transport_kind`를 고친다.

## 남은 실측

아직 기기에서 확인하지 않은 것. 확인되면 위 해당 절에 결과를 적고 여기서 지운다.

- `supportedValues`가 목록 항목을 숨기는지, 속성이 바뀌면 다시 그려지는지(#93, #113).
- `pc*.v10`(`pcWatchList`)의 감시 목록 카드 미리보기 세 칸("실행 중인 앱 · 감시 1 · 감시 2")이 각각 한 줄에 드는지, 영어 로케일에서 슬롯 값이 번역의 `Running`/`Stopped`/`Empty`로 바뀌는지(#123, "화면 배치"·"번역").
- 연결 거부와 시간 초과의 실제 오류 문구(위 "연결 거부와 시간 초과", 설계 §3.1).
- 프로필 이전의 착지 `infoChanged`에서 `args.old_st_store.profile.id`가 달라지는지(#129).
