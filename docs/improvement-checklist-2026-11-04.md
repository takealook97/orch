# 2026-11-04 평가 개선 체크리스트

원문: `/Users/deepeet/Desktop/orch-평가-2026-11-04.md` (평가 기준 커밋 `959eb7a`).
완료 표시는 구현과 회귀 검증 근거를 함께 남긴 뒤 갱신한다. 기존 기록으로 절감률을 입증했다고 주장하지 않는다.

## 결함
- [x] D1 DAG 완료에 공통 scope/commit/state 완료 게이트 적용
- [x] D2 worktree 없는 do도 최종 테스트 후 변경 재수집 및 scope 검사
- [x] D3 pane 닫기 실패 시 run active 유지 및 재시도 가능
- [x] D4 agy write_file/edit_file 본문과 실행 명령 구분
- [x] D5 runner/process group/pane 생존 판정 및 writer 보호 통일
- [x] D6 손상된 goal 상태에서 예산 게이트 fail closed
- [x] D7 DAG 원자적 저장, 실행 잠금, null/손상 상태 방어
- [x] D8 상태 저장 오류 전파 및 성공 오보고 제거
- [x] D9 동시 prompt 중복 전송 차단
- [x] D10 ask 캐시에서 질문 원문 보존
- [x] D11 herdr/hc/계정 CLI 제한 시간 및 자식 프로세스 정리

## 권고
- [x] R1 완료 처리 공통화 및 재현 결함 회귀 테스트
- [x] R2 생존·정리 처리 공통화
- [x] R3 Head/pane 토큰과 계약·검토·재작업 비용 기록, 누락값 명시
- [x] R4 직접/위임 및 1·5·15분 단일/병렬 반복 비교 지원
- [x] R5 검증의 신규 결함·오탐·반영 수정 및 실패 원인·회수 이벤트 기록
- [x] R6 direct/light/ask/do 시작 가이드, 고급 문서 분리, 폐기된 결정 표시
- [x] R7 소유 설정만 제거하는 uninstall 및 회귀 검증
- [x] R8 invalid --serves 등 오류의 복구 예시

- [x] P1 사용자 우선 정책: Head는 계획·평가·지시·리뷰·판단·소통만 담당; tiny/takeover 예외 제거 및 관리 문맥 실행 가드 회귀 검증

## 최종 확인
- [x] 독립 critical/neutral/adversarial 리뷰 결과 반영 (아래 재현·판정 참조)
- [x] make check 및 full race/Python 회귀 검증 (Finish repairs 최종 결과 참조)
- [x] 변경 커밋 및 원격 push 확인
- [x] 재설치, 설치본 smoke/doctor 검증
- [x] orch run 정리 확인

## Stage 1 구현·회귀 근거

소유 범위: 루트 `*.go` 및 이 체크리스트. 기존 미추적 체크리스트를 보존하고 완료 근거를 추가했다.
임시 감사 테스트 9개는 결함을 기대하는 단언을 정상 동작을 기대하도록 뒤집어 `audit_remediation_test.go`에 이식했다.
추가 안전 회귀는 `audit_safety_test.go`에 있다.

| 결함 | 구현 및 확인 근거 |
|---|---|
| D1 | wait/DAG/reaper가 `collectWorker` 완료 게이트를 공유한다. scope 확인, 선택 경로 커밋, 판정 저장 뒤 knowledge/report를 처리한다. `TestArchAuditDagScopeGate`는 위반 producer failed, dependent skipped, knowledge 오염 없음 확인. `TestAuditDAGCommitBeforeDependency`는 커밋 후 의존 gate 시작 확인. |
| D2 | `completionChanges`를 pane/do에서 공유하고, do는 최종 테스트 성공·실패·중단 여부와 무관하게 시도 직후 재수집한다. `TestArchAuditDoFinalScope`, `TestAuditDoTestScopeOnFailureAndIgnoredArtifacts`가 일반 checkout의 테스트 생성 파일, 실패한 테스트의 최종 diff, gitignore와 기존 baseline 보존 확인. |
| D3 | `EndRequested`를 먼저 저장하며 pane/overflow/필수 worktree 정리 실패 시 active 유지. reaper는 결과 파일 없이도 요청된 종료 재시도. `TestArchAuditCloseFailure`가 최초 실패와 후속 reaper 성공·closed 전환 확인. GC도 실패를 성공으로 기록하지 않는다. 미병합 worktree의 의도적 보존은 종료 실패가 아니다. |
| D4 | `write_file`/`edit_file`과 namespaced 형태를 편집으로 인식하고 본문을 commandText에서 제외. 실제 path 필드로 Head goal scope 검사. `TestAuditAgyBodiesAndPaths`와 기존 hook 테스트가 본문 내 명령 문자열 허용·실제 shell 명령 차단·Head 범위 이탈 차단 확인. |
| D5 | task writer는 `taskDead`와 최신 task.json의 PGID/heartbeat를 사용한다. Head 사망이 worker 보호를 해제하지 않으며 조회 실패·다른 backend는 unverified/live 취급. `TestArchAuditWriterGroup`, `TestAuditWriterUsesFreshChildGroupRecord`, 갱신된 worktree liveness/isolation/merge 테스트 확인. PGID 기록 실패 시 실행 그룹을 중지하고 오류 전파. |
| D6 | goal 읽기/의미 검증 오류를 admission/newRun/hook에서 숨기지 않는다. head 소유 메타데이터로 다른 Head의 손상 goal을 제외하며 복원 경로를 안내한다. `TestArchAuditCorruptGoal`, `TestAuditGoalApplicability`, 기존 goal/hook 테스트 확인. |
| D7 | DAG 상태는 파일 sync 후 임시 파일 rename. run-state lock과 별도의 scheduler flock 사용. null map/entry, JSON 손상, 알 수 없는 상태/노드 거부. launch intent를 먼저 저장하며 불확실한 시작/inline 재실행은 자동 반복하지 않는다. `TestArchAuditDagNull`, `TestAuditDagCorruptionAndAtomicFailure`, `TestAuditAtomicStateReaders`, `TestAuditDAGAcrossProcessesLaunchesOnce`, `TestAuditDAGUncertainLaunchIsNotRepeated` 확인. |
| D8 | close/status/report/DAG/task/PGID 필수 저장 실패를 호출자까지 전달한다. foreground/background task CLI 모두 최종 저장 오류에 실패 종료. 성공 캐시는 저장 완료 후에만 생성. `TestArchAuditClosePersistFailure`, `TestAuditFinalSaveFailureDoesNotCache`, DAG 저장 실패 회귀 확인. |
| D9 | worker별 별도 cross-process flock 안에서 최신 상태 재검사, baseline 보존, 전송 전 intent/hash 저장. 미전송이 확실한 approval 차단만 재시도 허용; 전송 결과 불확실 상태는 자동 재전송 금지. `TestArchAuditConcurrentPrompt`, `TestAuditPromptAcrossProcesses`, `TestAuditPromptUncertainKeepsBaseline`, `TestAuditPromptBlockedBeforeSendIsRetryable`, 기존 계약 hash 회귀 확인. |
| D10 | 버전이 다른 캐시 키에 질문 원문 바이트를 사용. `TestArchAuditCacheCollision`, 갱신된 `TestAskCacheKeyInvalidation` 확인. |
| D11 | herdr/hc/herdr-app/계정 명령에 공통 제한 시간·프로세스 그룹 종료·출력 상한·WaitDelay 적용. wait 명령은 요청한 시간에 여유를 더하며 hc RPC pipe도 deadline에 닫는다. 일반 task/test 실행도 정상 launcher 종료 후 남은 그룹 정리. `TestAuditControlTimeoutKillsDescendants`, `TestAuditControlWrappersAndWaitDeadlines`, `TestAuditAccountTimeoutRedactsValues`와 기존 timeout/interrupt 회귀 확인. |

### 설계 도전 검토와 의도적 제한

- 새 repair/reset CLI, PID 시작 시각 스키마, 임의 unknown 만료 정책은 이번 결함 수정에 필요한 근거가 없어 도입하지 않았다. 작은 기존 호환 helper를 사용한다.
- 손상 상태 파일을 자동 격리하거나 초기화하지 않는다. 원본을 보존하고 복원·확인할 경로를 오류에 표시한다. 소유자를 확인할 수 없는 구형 손상 goal은 보수적으로 admission을 막는다. 해당 디렉터리를 검토해 복원하거나 별도 위치로 옮겨야 한다.
- 만료 시간만 보고 prompt 예약을 재획득하면 중복 실행과 baseline 오염이 가능하므로 채택하지 않았다. 불확실 전송은 `orch read`로 확인 후 닫고 새 worker 이름으로 복구한다. 같은 계약의 정상 동시 호출은 한 번만 전송한다.
- 중단된 inline DAG 노드와 시작 여부가 불확실한 노드는 증거를 검토해 DAG 상태를 수동으로 조정해야 한다. 자동 재실행보다 중복 부작용 방지를 우선한다.
- 프로세스 그룹 밖으로 `setsid` 등으로 이탈한 자손의 추적까지 구현한 것은 아니다. 별도 프로세스 감독 계층은 후속 범위다.
- Stage 1 시점에는 측정/eval·uninstall·문서 재구성 및 `invalid --serves` 개선은 R3–R8로 남긴다. 독립 리뷰·make check·커밋/push·재설치·실제 설치본 검증도 이 worker 단계의 완료로 표시하지 않는다.

### 실행 검증

- 전체 `go test ./...` 통과 (55.397s).

- 대상 회귀: `go test -run 'TestAudit|TestArchAudit|TestGoal|TestOverflow|TestClose|TestWait|TestReap|TestLiveWriters|TestBackgroundWriter' -count=1 ./...` 통과.
- 경쟁 검사: `go test -race -run 'TestAudit|TestArchAudit|TestKnowledgeConcurrentMerge|TestConcurrentWriterAdmission' -count=1 ./...` 통과.
- `go vet ./...`, `git diff --check` 통과.

## Stage 2 구현·회귀 근거

기존 D1–D11 변경을 보존했다. stage 2는 같은 checkout의 단일 writer로 수행했고 위임·실제 설치·커밋·push·라이브 모델 벤치마크는 실행하지 않았다.

| 항목 | 구현·증거 |
|---|---|
| R3 | `measurement.go`: `measure import/report`, version/session/period/entity/source/model, nullable input/output/duration, measured/estimate/missing, complete/partial/unavailable, phase contract/execution/review/rework. flock+fsync, ID 중복 멱등/충돌 거부, malformed log 보존. `TestMeasurementEvidence`는 null/실측 0, coverage, 동시 중복, 충돌·손상·연결 없는 accepted_fix 거부 확인. |
| R4 | `eval/experiment.py`, `delegate.py`, `run.py`: A=top-tier worker proxy, actual Head=null, 추정 Head 별도 필드. seeded counterbalance 반복, fresh ask, 새 checkout, schedule/dry-run, intent/raw/result/summary/report 재구성. 1/5/15 목표 workload는 실제 함수 3/15/45개와 hidden checks, single/parallel pane+별도 worktree, 실측 wall/worker spans/first-start delay/품질/coverage/raw DAG/model identity/cleanup 기록. `eval/test_experiment.py`와 기존 fixture selfcheck. 실시간 보정·절감 입증을 했다는 주장은 없다. |
| R5 | import event enum new_defect/false_positive/accepted_fix/failure/recovery_started/recovered/recovery_failed, reason code, related ID 검증 및 event 집계. ask/do/test 실행 결과와 pane 완료 실패, fallback 시작 및 완료 게이트 회수 이벤트 자동 기록. `TestPaneRecoveryEvidence`는 실제 연결·멱등·legacy 시작 이벤트 비조작 확인. 확인된 결함·오탐·수정 반영 판정은 사람이 근거를 검토해 import한다. |
| R6 | README는 direct/light+ask/do/test 앞 배치, pane flow/account/layout은 `skill/references/advanced.md`로 이동. skill 앞에 짧은 시작 안내. pane 재사용 두 문구 삭제, 옛 6-key 및 inline review 기본 결정 obsolete 표시, 독립 review는 새 pane으로 일치. generated catalog blocks 보존. |
| R7 | `uninstall.go`, `install_ownership.go`: default dry-run, --apply, exact block/commands/links 및 manifest, 모든 입력 preflight, 변경 전 snapshot 재확인·백업, atomic config 쓰기. 사용자 mixed hooks/foreign links/실제 디렉터리/비정규 config/상태 보존. reinstall 및 Makefile link도 foreign 경로 덮어쓰기 거부. `TestUninstallOwnership`(mixed/empty user groups 포함), `TestUninstallFailsSafe`, `TestInstallUninstallManifestAndForeignData`, `TestInstallForeignBinaryPreflight`. |
| R8 | `parseServes`가 양수 criterion 및 `--serves 1`, `--serves 1,2`, `orch goal status` 복구 예시를 안내. `TestServesRecoveryExamples`가 양수·형식 및 복구 메시지 확인. |
| P1 | constitution/skill/design에서 작은 직접 실행·takeover·직접 merge 예외 제거. `head_guard.go`가 관리 run/명시 opt-in 문맥의 알려진 edit/patch/shell 실행을 redirect; inspection/orch 및 run contracts/acceptance/decisions/findings 허용. worker/일반 세션에 적용하지 않음. `TestHeadExecutionGuardManaged`가 tiny edit/build/test/Git/interpreter/redirection/raw-prefix chain·허용 경로·일반 세션·worker를 확인. |

정확한 사용법과 경계는 `docs/operations.md`에 있다. 자동 provider transcript import는 지원하지 않으며 명시적 수동 evidence import가 경로다. 기간 범위의 정확성·중복되지 않는 관측 ID는 import 작성자가 보장한다. unsupported 실제 Head 관측은 계속 누락이다. 실행 가드는 shell 보안 경계가 아니며 임의 alias/script/unknown tool을 모두 판별하지 않는다. direct 모드 Herdr에서는 `ORCH_HEAD_GUARD=1`로 명시 활성화할 수 있다. uninstall은 원래 matcher/JSON 공백/Codex trust를 복원하지 않으며 외부 동시 config writer와의 완전한 트랜잭션을 보장하지 않는다. 독립 리뷰·실제 설치본 smoke/doctor는 이 worker가 완료했다고 표시하지 않는다.

추가 검증 경계: `TestCodexPromptInstall`의 빈 임의 binary fixture를 실행 중인 test binary의 소유 symlink로 변경해 새 foreign-binary 보존 규칙과 일치시켰다(기존 hook 신뢰·멱등 단언은 유지). 이 worker가 생성한 pane은 없으며 부모 run 종료/독립 리뷰는 Head의 후속 단계다.

## Finish repairs: 리뷰 재현 및 판정

`tasks/task-5/answer.md`, `tasks/task-6/answer.md`, `results/adversarial.md`, `results/critical-google-focus.md`, `results/stage2-critical.md`, `results/stage2-neutral.md`를 모두 읽었다. Stage 2 critical/neutral은 작업 도중 도착했고 최종 검증 전에 반영했다. 기존 구현은 보존하고 동일 checkout의 sole writer로 수정했다. 이 절의 판정이 앞선 Stage 1/2 시점 설명보다 우선한다.

| 주장/실제 회귀 | 재현·수정·판정 |
|---|---|
| 설치 fixture 실패 | `TestCodexPromptInstall`의 실행 binary symlink 수정은 이미 있었고 재실행 통과. `TestInstallForeignBinaryPreflight`는 foreign symlink와 일반 파일 모두 거부함을 확인. 소유권 보호를 완화하지 않았다. |
| Head의 quoted do/test 요구 거부 | `TestFinishHeadQuotedDelegation`에서 인자 안 `; go test`, `make`, `>`가 거부되는 실패를 먼저 확인. quote 안 orch 인자는 데이터로 처리하되 실제 chain/redirect/command substitution은 계속 검사한다. backtick 실행 누락도 재현·차단. |
| wait accepted-empty race / reaper 거짓 close | 기존 사전 agentStopped 검사는 있었지만 수집 시점 상태 변경을 막지 못했다. `TestFinishWaitStateRace`에서 빈 outcome을 재현하고 pending 유지로 수정. `TestFinishReaperStateRace`는 수집 시 blocked가 된 pane을 보존하고 실제 Pane 제거 확인 전 Reaped/log/closed를 기록하지 않음을 확인. |
| Git pathspec 오염 | `TestFinishLiteralCompletion`의 `*.txt`, `:(glob)*.txt`가 unrelated staged 경로까지 commit하는 실패를 먼저 확인. diff/ls-files/add/commit 모두 `--literal-pathspecs` 적용. `[id].txt`도 확인하며 unrelated index와 재시도 멱등성 회귀를 유지했다. |
| promptNotSent 계약 변경 거부 | `TestAuditPromptBlockedBeforeSendIsRetryable`에 hash 초기화와 변경된 계약 재시도를 추가했다. 확실히 미전송인 경우만 PromptState/ContractHash를 함께 비우고 baseline은 보존한다. 전송 불확실/느린 부팅은 중복 실행을 피하려 자동 재전송하지 않는다. |
| edit suffix 우회 / path fail-open | `mcp__term__write`, `x.edit`의 worker delegation 우회와 missing path 허용을 재현. 정확히 알려진 edit/patch 도구만 본문을 제외하고 `absolute_path`를 지원하며 경로가 없으면 goal scope 검사를 거부한다. 가상 namespace fixture는 실제 API 이름으로 바꿔 기존 scope 단언을 유지했다. 알 수 없는 임의 도구 전체를 sandbox로 취급하지 않는다. |
| DAG Ctrl-C 재개 불가 | 현재 구현에는 이미 confirmed-interrupted 노드를 pending으로 저장하는 수정이 있었다. `TestFinishDagInterruptedResume`는 별도 프로세스의 inline command 시작 확인 → SIGINT → child 종료 확인 → pending 저장 → 재실행 done을 검증. 불확실 launch/inline running 상태는 계속 수동 확인을 요구한다. |
| knowledge 영구 실패/중복 | `mergeKnowledge`는 fact key 중복 제거로 멱등. `TestFinishKnowledgeFailureRetry`는 merge I/O 오류 전파, pane 선종료, knowledge.md 복원 뒤 동일 worker 재수집 성공 및 fact 1개만 유지 확인. 오류를 숨기거나 손상 파일을 자동 초기화하지 않는다. |
| closed run 재활성화 | closed run에 end를 반복하면 cleanup을 다시 호출하는 경로를 제거. `TestFinishClosedRunStaysClosed`는 cleanup 호출 자체가 없고 closed 유지 확인. GC의 별도 잔여 pane 정리 경로는 유지. |
| missing/corrupt task.json cleanup 정체 | unverified live writer 보호는 의도한 안전 조건. `TestFinishUnknownTaskPreserved`에서 보존/오류를 재현하고 task state 복원·process/pane 확인·run end 재시도 안내를 추가. 임의 만료나 미검증 삭제는 도입하지 않았다. |
| 다른 goal/.DS_Store에 의해 모든 Head 차단 | non-directory entry는 이미 제외된다. 소유 sidecar/유효 owner로 다른 Head goal 제외는 기존 `TestAuditGoalApplicability`로 확인. 소유자조차 불명인 손상 legacy goal은 보수적으로 admission을 막는 의도한 제한이며 복원 안내를 유지. |
| reaped PID 재사용 위험 | 실제 무관 process group 종료 재현 근거가 없는 이론적 주장으로 미채택. 정상 launcher 뒤 남은 자손 정리와 timeout 회귀를 유지. setsid로 이탈한 자손 추적은 여전히 범위 밖이다. |
| task-7 denied/empty인데 done/cache | 실제 task-7 output.log는 permission warning 뒤 SUCCESS/empty response/denied_actions였다. `TestFinishProviderFailure`, `TestFinishProviderMixedLog`에서 zero-exit false success 재현. structured envelope를 답변으로 fallback하지 않고 denied/empty는 failed, do 최종 test/commit 금지, 변경 scope 수집은 유지. `TestFinishLegacyDeniedCache`는 신규 저장과 오래된 denied/empty 캐시 재사용 모두 거부 확인. 전역 permission은 변경하지 않았다. |
| run/dag.yaml 계획 편집 거부 | `TestFinishHeadDagPlan`에서 실패 재현 후 정확한 run/dag.yaml 허용. `TestFinishHeadDraftPlan`은 현재 Head newest draft의 goal.md만 허용하고 goal.json, 다른 goal, frozen goal 편집을 거부. README/skill/constitution/operations 일치시켰다. |
| non-Git do raw git diff 129 | `TestFinishNonGitDo`로 재현. 평가 base `959eb7a`에서도 baseline/final scope는 changedFiles의 Git에 의존했다. Git 없는 write 추적을 새로 우회하지 않고 provider 실행 전 Git repository 요구와 --cwd/직접 planning 편집 복구 안내를 출력한다. |
| 측정 오류가 성공 실행을 실패로 바꿈 | 손상 records.jsonl로 ask done/exit 1을 `TestFinishMeasurementFailureIsWarning`에서 재현. 자동 task/test/pane/recovery 측정은 경고로 분리; 실제 task/DAG/run state 저장 실패는 계속 fatal. 명시 measure import/report의 손상 오류는 계속 전파하고 원본 보존. O_APPEND 부재 자체는 flock 안에서 기존 JSON을 EOF까지 읽고 append하므로 overwrite 근거가 아니며 partial crash log는 fail-closed 복원 대상이다. |
| uninstall manifest 및 legacy ownership | `TestFinishUninstallReleasesManifest`에서 uninstall 후 manifest 잔존을 재현. matching manifest는 snapshot/backup 후 마지막에 제거, A uninstall → B install 확인. `TestFinishLegacyForeignUninstall`에서 foreign 공유 binary 소유 hook/block 제거를 재현하고 legacy shared link의 repo 일치가 확인될 때만 제거하도록 수정. |
| uninstall JSON 정밀도 손실 | `TestFinishUninstallPreservesNumbers`는 9007199254740993 → 9007199254740992 변환을 재현. UseNumber/HTML escape 비활성 encoder로 값 보존. whitespace/key 순서 복원은 약속하지 않는다. |
| Head 명령 누락·읽기 오탐 | `TestFinishHeadGuardCommands`에서 >&out, go -C, git -c/--git-dir 및 나열된 mutation/file/package 명령 누락과 grep stderr/awk/git log 문자열 오탐을 먼저 재현. 알려진 명령 검사 확대, quoted inspection 데이터 및 /dev/null/descriptor redirect 구분. symlink 경유 edit는 기존 goalPath가 실제 경로를 해석하므로 추가 우회 근거 없음. |
| eval 실패 품질 분모·import credit | exception quality_total=1을 실제 fixture case 수로 교정. `test_exception_keeps_expected_quality_denominator` 확인. 실제 missing module unittest 실행에서 옛 수식은 cases-1 통과를 반환함을 재현; `test_failed_import_earns_no_partial_credit`는 loaded suite의 정확한 실행 수를 확인하고 import/loader 오류를 0점 처리. timeout은 DAG CLI의 millisecond 단위와 일치. |
| 측정 grouping이 worker 수와 다름 | n_total은 evidence row 수이며 worker/시도 수가 아니다. recovery 시작/완료는 source도 달라 서로 다른 group이다. measured duration과 missing token coverage는 별개이며 보고서는 누락 token 수를 별도 표기한다. catalog-key fallback은 실제 ModelID를 발명하지 않는 기존 한계로 문서화. |

### 검증 중 발견·교정

- 초기 provider 회귀의 mock executable을 gemini로 잘못 지정해 실제 agy 호출 3회가 발생했다. 출력의 command 권한 거부를 확인했고 fixture를 실제 실행 파일인 agy로 교정했다. 이후 provider 회귀는 로컬 mock만 사용했다. permission 설정 변경·라이브 benchmark는 하지 않았다.
- 첫 make check/full race는 기존 `ns.NotebookEdit` 가상 namespace fixture가 precise allowlist에서 더 이상 edit로 분류되지 않아 실패했다. 실제 `NotebookEdit`로 교정하고 기존 outside-scope 거부 단언은 유지했다. 신규 회귀는 unknown edit suffix의 실행 본문이 검사됨을 별도로 확인한다.
- worker hook이 테스트/문서 heredoc의 문자열을 실행 명령으로 오인해 도구 호출을 거부한 경우에는 편집 도구로 같은 owned 파일을 작성했다. hook/permission 설정을 우회하거나 변경하지 않았다.

최종 make check/full race/Python 결과는 아래에 기록한다. commit/push/install 및 부모 run 정리는 여전히 Head 후속 작업이다. 이 worker는 pane을 열지 않았다.

### Finish repairs 최종 실행 결과

- `make check`: exit 0. build, generated docs diff, go vet, 전체 go test(56.096s), playbook schema check 모두 통과.
- `go test -race ./...`: exit 0, 전체 suite 62.552s, race 보고 없음.
- `python3 -B -m unittest discover -s eval -p 'test_*.py'`: exit 0, 7 tests, 0.222s.
- `python3 -B eval/run.py selfcheck`: exit 0; delegate 18 fixtures(6종 각 3), review 12, parallel 8 통과.
- `git diff --check`: exit 0.
- 범위 밖 쓰기·delegation·commit·push·실제 install/uninstall·부모 run 정리 없음. install/uninstall 회귀는 임시 HOME/repo만 사용했다. 명시 기록한 초기 provider mock 실수 외 검증은 로컬 fixture이며 라이브 비용 비교를 수행하지 않았다.

## Final narrowly scoped hardening

`results/final-critical.md`의 남은 세 가지 입증된 회귀만 수정했다. 앞선 변경을 보존한 sole code writer이며 별도 위임·commit·push·install 및 라이브 provider 실행은 하지 않았다.

| 항목 | 재현 및 수정 근거 |
|---|---|
| MCP filesystem 경로 검사 누락 | `TestFinalMCPFilePaths`에서 정확한 `mcp__filesystem__write_file`/`mcp__filesystem__edit_file`의 missing/outside path가 Head/goal 검사를 통과하는 실패를 먼저 재현. `TestFinalMCPCompilePaths`는 Codex goal 컴파일 중 orch planning 경로조차 허용되지 않는 회귀를 재현. path-edit 인식을 body exemption과 분리하여 세 경로 검사에만 정확한 MCP 이름을 추가했다. `TestFinalTerminalWriteStillInspected`는 terminal.write 계열 및 MCP의 명령 본문이 worker no-delegation 검사에서 사라지지 않음을 확인. |
| inspection masking 실행 누락 | `TestFinalHeadInspectionPrograms`에서 quoted awk system/print redirects/pipes, sed w/e 및 substitution flags, Go mod/get, find delete/exec, shell script 실행 누락을 먼저 재현. quoted inspection을 가리기 전 좁은 awk/sed 검사를 수행하고 나열된 명령을 추가했다. read-only awk 비교·괄호 안 print 비교·문자열 format, grep pattern, Git log format, sed replacement 및 quoted orch 요구 인자는 허용됨을 확인. |
| fakeCLI의 실제 provider PATH fallback | `TestFinalFakeCLISentinels`는 실제 agent 대신 harmless trailing PATH stand-in만 놓고 잘못된 mock 이름이 이 stand-in까지 도달함을 먼저 재현. `codex`/`claude`/`agy`/`gemini`/`cr` fail-closed sentinel을 원래 PATH 앞에 두어 누락/오타 mock을 실패시키고, 명시 mock 여러 개와 system tooling을 유지한다. |

### 검증 기록 및 한계

- 최초 `go test -run '^TestFinal' -count=1 ./...`: 위 세 회귀의 실패를 확인. provider fallback 재현도 harmless stand-in만 호출했다.
- 최종 focused suite: `go test -run '^TestFinal|TestFinishHead|TestFinishEdit|TestHeadExecutionGuardManaged|TestGoalScope|TestCodexGoalCompileGuard|TestDag' -count=1 ./...` 통과 (2.913s).
- 첫 `make check`는 기존 `TestTaskCapacityResume/codex/cap`의 1초 fixture timeout으로 실패 (62.833s). `go test -run '^TestTaskCapacityResume/codex/cap$' -count=3 ./...`는 3회 모두 통과 (1.315s). timeout/기존 단언은 변경하지 않았다.
- 최종 source 기준 `make check` 재실행: exit 0, build/generated docs diff/go vet/전체 go test (56.285s)/playbook schema check 통과.
- `go test -race ./...`: 요청대로 전체 race 1회 실행, exit 0 (60.214s), race 보고 없음.
- 최종 `git diff --check`: exit 0. 이 단계의 source/doc 변경은 `hook.go`, `head_guard.go`, `task_test.go`, `final_hardening_test.go`, 이 checklist 및 `docs/operations.md`뿐이다. 나머지 기존 변경은 보존했고 audit script를 수정하지 않았다.
- guard는 열거한 알려진 도구·일반 프로그램 형태의 workflow redirect이며 shell/awk/sed 전체 문법의 보안 sandbox가 아니다. alias, opaque script 및 임의 unknown tool 실행을 모두 판별한다는 주장은 하지 않는다. 앞선 task-state 복원/setsid 등 범위 밖 한계는 그대로다.
- audit에 함께 언급됐으나 이번 선택된 command 추가 범위에 포함되지 않은 `exec 3<>file` 같은 descriptor read/write 구문은 계속 포괄하지 않는다.

### Head 최종 검증

- Head가 최종 리뷰를 수락했으며 기존 make check, race, Python 검증 기록을 확인했다.

- 설치 및 정리 결과: 변경 커밋 push 및 원격 HEAD 일치, make install, 설치본 help/models/uninstall dry-run, doctor 통과, 지정된 두 run의 pane 제거 확인.

- Orchestration note: task-10 scope report included concurrent final-hardening writes during ignored script preparation; Head corrected to serial execution; source audit suite passed.
