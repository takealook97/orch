# orch run 보고서 기능 (cockpit Report 토글 + 브라우저 HTML)

## 결론

`orch report` 와 cockpit Report 버튼을 두 저장소에 넣고 이 Mac 에 다시 설치했습니다.

Herdr 안에서 직접 눈으로 확인하는 일만 남았습니다. 두 저장소 모두 아직 커밋하지 않았습니다.

## 한 일

- `orch report`: 기본은 report.md, `--json`, `--html [--open]` 을 고를 수 있습니다.
- HTML 은 디핏 디자인시스템(f98b6ac) 기반이고 로고는 없습니다. 차트 두 개는 Go 가 **SVG** 로 그립니다.
- cockpit 상단 바 순서를 `계정 → 여백 → Linear → Report → system → 기어` 로 바꿨습니다.

## 검증

리뷰는 작성자와 다른 계열이 맡았습니다.

| 단계 | 모델 | 결과 |
| --- | --- | --- |
| 기계 검사 | orch test | orch `make test && go vet` 통과, cockpit 376 통과 |
| 비판 리뷰 | codex-high-l | 3회, 확인된 결함 고침 |
| 중립 리뷰 | gemini-medium-m | HTML 라이트·다크·인쇄 통과 |

## 남은 위험

- [중간] Hub 를 확대하는 중에 Report 를 누르면 반영이 레이아웃 쓰기 한 번만큼 늦어집니다.
- [낮음] 상단 바가 320px 까지 좁아지면 오른쪽 버튼이 약 34px 모자랍니다.
- 등급 표시가 없는 위험 항목.

## 확인할 것

- [ ] Report 클릭으로 보고서 화면과 코드 화면이 오가는지
- [x] 설정에서 Linear 를 끄면 Report 가 그 자리로 오는지
- "브라우저에서 보기"

## 메모

자유 형식 문단입니다. *강조* 와 [링크](https://example.com) 가 들어 있습니다.
