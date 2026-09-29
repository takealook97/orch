"""P7 regression eval set, plus 18 Head-direct/delegation fixtures.

review   (12): seeded-bug Python modules. Same-family vs cross-family reviewers must report the
               planted defect; detection is judged by the reported line numbers (BUG_LINES).
parallel  (8): three independent functions per task. One worker vs three parallel workers;
               judged by hidden unit tests and wall time.
"""

# Each delegate fixture starts from `files`; `solution` is used only by selfcheck.
# Code checks are independent Python expressions evaluated after importing target.
# Research checks apply to the returned answer; document checks apply to the draft.
DELEGATE = [
    {
        "id": "lookup-config", "type": "research", "mode": "ask",
        "files": {"config.txt": "timeout_ms=4500\nretries=3\nregion=ap-northeast-2\n"},
        "prompt": "Read config.txt. State timeout_ms, retries, and region in one short answer.",
        "solution": "timeout_ms=4500; retries=3; region=ap-northeast-2",
        "checks": ["timeout_ms=4500", "retries=3", "region=ap-northeast-2"],
    },
    {
        "id": "lookup-api", "type": "research", "mode": "ask",
        "files": {"API.md": "POST /v1/jobs creates a job.\nGET /v1/jobs/{id} reads one.\nDELETE /v1/jobs/{id} cancels one.\n"},
        "prompt": "Read API.md. Give the create, read, and cancel method/path pairs.",
        "solution": "POST /v1/jobs; GET /v1/jobs/{id}; DELETE /v1/jobs/{id}",
        "checks": ["post /v1/jobs", "get /v1/jobs/{id}", "delete /v1/jobs/{id}"],
    },
    {
        "id": "lookup-release", "type": "research", "mode": "ask",
        "files": {"CHANGELOG.md": "v2.4: added CSV export.\nv2.5: fixed timezone parsing.\nv2.6: removed legacy XML import.\n"},
        "prompt": "Read CHANGELOG.md. State what changed in versions 2.4, 2.5, and 2.6.",
        "solution": "2.4 CSV export; 2.5 timezone parsing; 2.6 legacy XML import removed",
        "checks": ["2.4", "csv export", "2.5", "timezone parsing", "2.6", "xml import", "removed"],
    },
    {
        "id": "impl-slug", "type": "implementation", "mode": "do",
        "files": {"target.py": "def slug(text):\n    raise NotImplementedError\n"},
        "prompt": "Implement slug(text) in target.py: lowercase ASCII text, replace every run of non-alphanumeric characters with one hyphen, strip edge hyphens.",
        "solution": {"target.py": "import re\n\ndef slug(text):\n    return re.sub(r'[^a-z0-9]+', '-', text.lower()).strip('-')\n"},
        "checks": ["target.slug('Hello, World!') == 'hello-world'", "target.slug(' A__B ') == 'a-b'", "target.slug('---') == ''"],
    },
    {
        "id": "impl-clamp", "type": "implementation", "mode": "do",
        "files": {"target.py": "def clamp(value, low, high):\n    raise NotImplementedError\n"},
        "prompt": "Implement clamp(value, low, high) in target.py. Raise ValueError when low > high; otherwise restrict value to the inclusive interval.",
        "solution": {"target.py": "def clamp(value, low, high):\n    if low > high:\n        raise ValueError('bounds')\n    return min(high, max(low, value))\n"},
        "checks": ["target.clamp(5, 0, 3) == 3", "target.clamp(-2, 0, 3) == 0", "target.clamp(2, 0, 3) == 2", "_raises(target.clamp, ValueError, 1, 3, 2)"],
    },
    {
        "id": "impl-flatten", "type": "implementation", "mode": "do",
        "files": {"target.py": "def flatten(rows):\n    raise NotImplementedError\n"},
        "prompt": "Implement flatten(rows) in target.py. Concatenate one level of iterables in order; preserve duplicates and accept empty rows.",
        "solution": {"target.py": "def flatten(rows):\n    return [item for row in rows for item in row]\n"},
        "checks": ["target.flatten([[1, 2], [], [2]]) == [1, 2, 2]", "target.flatten([]) == []", "target.flatten([('a',), ('b', 'c')]) == ['a', 'b', 'c']"],
    },
    {
        "id": "test-even", "type": "test_fix", "mode": "do",
        "files": {"target.py": "def is_even(n):\n    return n % 2 == 1\n", "test_target.py": ""},
        "prompt": "Fix is_even in target.py and write unittest tests in test_target.py covering zero, negative even, and odd values.",
        "solution": {"target.py": "def is_even(n):\n    return n % 2 == 0\n", "test_target.py": "import unittest\nfrom target import is_even\n\nclass Tests(unittest.TestCase):\n    def test_zero(self): self.assertTrue(is_even(0))\n    def test_negative(self): self.assertTrue(is_even(-2))\n    def test_odd(self): self.assertFalse(is_even(3))\n"},
        "checks": ["target.is_even(0) is True", "target.is_even(-2) is True", "target.is_even(3) is False", "_tests_pass()", "_test_count() >= 3", "_tests_reject_bug()"],
    },
    {
        "id": "test-space", "type": "test_fix", "mode": "do",
        "files": {"target.py": "def normalize_spaces(s):\n    return s.strip()\n", "test_target.py": ""},
        "prompt": "Fix normalize_spaces to collapse all whitespace runs to one space and trim edges. Add unittest tests for tabs, newlines, and empty input.",
        "solution": {"target.py": "def normalize_spaces(s):\n    return ' '.join(s.split())\n", "test_target.py": "import unittest\nfrom target import normalize_spaces\n\nclass Tests(unittest.TestCase):\n    def test_tabs(self): self.assertEqual(normalize_spaces(' a\\tb '), 'a b')\n    def test_newlines(self): self.assertEqual(normalize_spaces('a\\nb'), 'a b')\n    def test_empty(self): self.assertEqual(normalize_spaces('  '), '')\n"},
        "checks": ["target.normalize_spaces(' a\\tb ') == 'a b'", "target.normalize_spaces('a\\nb') == 'a b'", "target.normalize_spaces('  ') == ''", "_tests_pass()", "_test_count() >= 3", "_tests_reject_bug()"],
    },
    {
        "id": "test-divide", "type": "test_fix", "mode": "do",
        "files": {"target.py": "def safe_divide(a, b):\n    return a / b\n", "test_target.py": ""},
        "prompt": "Fix safe_divide to return None when divisor is zero, otherwise true division. Add unittest tests for zero, negative, and fractional results.",
        "solution": {"target.py": "def safe_divide(a, b):\n    return None if b == 0 else a / b\n", "test_target.py": "import unittest\nfrom target import safe_divide\n\nclass Tests(unittest.TestCase):\n    def test_zero(self): self.assertIsNone(safe_divide(1, 0))\n    def test_negative(self): self.assertEqual(safe_divide(-4, 2), -2)\n    def test_fraction(self): self.assertEqual(safe_divide(1, 2), 0.5)\n"},
        "checks": ["target.safe_divide(1, 0) is None", "target.safe_divide(-4, 2) == -2", "target.safe_divide(1, 2) == 0.5", "_tests_pass()", "_test_count() >= 3", "_tests_reject_bug()"],
    },
    {
        "id": "build-colon", "type": "build_fix", "mode": "do",
        "files": {"target.py": "def double(n)\n    return n * 2\n"},
        "prompt": "Fix target.py so it imports and double(n) returns n * 2.",
        "solution": {"target.py": "def double(n):\n    return n * 2\n"},
        "checks": ["target.double(0) == 0", "target.double(-3) == -6", "target.double(2.5) == 5"],
    },
    {
        "id": "build-name", "type": "build_fix", "mode": "do",
        "files": {"target.py": "def total(values):\n    return sum(items)\n"},
        "prompt": "Fix target.py's name error. total(values) must sum values, including empty input.",
        "solution": {"target.py": "def total(values):\n    return sum(values)\n"},
        "checks": ["target.total([]) == 0", "target.total([2, 3, -1]) == 4", "target.total((1.5, 2.5)) == 4"],
    },
    {
        "id": "build-type", "type": "build_fix", "mode": "do",
        "files": {"target.py": "def join_ids(ids: list[int]) -> str:\n    return ','.join(ids)\n"},
        "prompt": "Fix target.py's type error. join_ids accepts integers and returns comma-separated decimal strings, including an empty list.",
        "solution": {"target.py": "def join_ids(ids: list[int]) -> str:\n    return ','.join(str(i) for i in ids)\n"},
        "checks": ["target.join_ids([1, -2, 3]) == '1,-2,3'", "target.join_ids([]) == ''", "target.join_ids([0]) == '0'"],
    },
    {
        "id": "refactor-score", "type": "refactor", "mode": "do",
        "files": {"target.py": "def score_old(points):\n    return sum(points)\n"},
        "prompt": "Rename score_old to total_score in target.py, preserving behavior. Remove the old name.",
        "solution": {"target.py": "def total_score(points):\n    return sum(points)\n"},
        "checks": ["target.total_score([2, -1, 4]) == 5", "not hasattr(target, 'score_old')"],
    },
    {
        "id": "refactor-label", "type": "refactor", "mode": "do",
        "files": {"target.py": "def label_old(name):\n    return '[' + name.strip() + ']'\n"},
        "prompt": "Rename label_old to format_label in target.py, preserving behavior. Remove the old name.",
        "solution": {"target.py": "def format_label(name):\n    return '[' + name.strip() + ']'\n"},
        "checks": ["target.format_label(' a ') == '[a]'", "not hasattr(target, 'label_old')"],
    },
    {
        "id": "refactor-count", "type": "refactor", "mode": "do",
        "files": {"target.py": "def count_old(items):\n    return len(items)\n"},
        "prompt": "Rename count_old to item_count in target.py, preserving behavior. Remove the old name.",
        "solution": {"target.py": "def item_count(items):\n    return len(items)\n"},
        "checks": ["target.item_count([]) == 0", "target.item_count([1, 2]) == 2", "not hasattr(target, 'count_old')"],
    },
    {
        "id": "doc-backup", "type": "doc_draft", "mode": "do",
        "files": {"NOTES.md": "Backup tool: `backup --source SRC --dest DST`. Restore tool: `restore --source SRC --dest DST`. Verify with `verify --dest DST`.\n", "GUIDE.md": ""},
        "prompt": "Draft GUIDE.md from NOTES.md. Include the exact backup, restore, and verify commands.",
        "solution": {"GUIDE.md": "# Backup guide\n\nRun `backup --source SRC --dest DST`. Restore with `restore --source SRC --dest DST`. Check with `verify --dest DST`.\n"},
        "checks": ["backup --source src --dest dst", "restore --source src --dest dst", "verify --dest dst"],
    },
    {
        "id": "doc-release", "type": "doc_draft", "mode": "do",
        "files": {"NOTES.md": "Release steps: run `make check`; create tag `v2.0`; publish artifact with `upload dist/app.tar.gz`.\n", "GUIDE.md": ""},
        "prompt": "Draft GUIDE.md from NOTES.md. Include all three release steps and exact commands.",
        "solution": {"GUIDE.md": "# Release\n\nRun `make check`, create tag `v2.0`, then `upload dist/app.tar.gz`.\n"},
        "checks": ["make check", "v2.0", "upload dist/app.tar.gz"],
    },
    {
        "id": "doc-oncall", "type": "doc_draft", "mode": "do",
        "files": {"NOTES.md": "On-call: inspect `health /ready`, examine `logs --since 10m`, then escalate to `#infra` if errors persist.\n", "GUIDE.md": ""},
        "prompt": "Draft GUIDE.md from NOTES.md. Include the health, logs, and escalation steps.",
        "solution": {"GUIDE.md": "# On-call\n\nCheck `health /ready`, inspect `logs --since 10m`, and escalate to `#infra` if errors persist.\n"},
        "checks": ["health /ready", "logs --since 10m", "#infra"],
    },
]

REVIEW = [
    {
        "id": "bsearch",
        "spec": "search(xs, t): xs is a sorted list of integers in ascending order. Return the index of t, or -1 if absent. Handle empty lists and the last element.",
        "code": """def search(xs, t):
    lo, hi = 0, len(xs) - 1
    while lo < hi:
        mid = (lo + hi) // 2
        if xs[mid] == t:
            return mid
        if xs[mid] < t:
            lo = mid + 1
        else:
            hi = mid - 1
    return -1
""",
        "bug_lines": [3],
        "bug": "while lo < hi skips the final candidate when lo == hi, so single-element lists and the last element are missed",
    },
    {
        "id": "movavg",
        "spec": "moving_average(xs, k): Return the list of averages for windows of length k (len(xs)-k+1 values). Return an empty list if k <= 0 or k > len(xs).",
        "code": """def moving_average(xs, k):
    if k <= 0 or k > len(xs):
        return []
    out = []
    for i in range(len(xs) - k + 1):
        window = xs[i:i + k + 1]
        out.append(sum(window) / k)
    return out
""",
        "bug_lines": [6],
        "bug": "The slice takes k+1 elements, producing the wrong sum",
    },
    {
        "id": "daterange",
        "spec": "days_between(start, end): Given two datetime.date values, return a list of dates from start through end, inclusive. Return an empty list if start > end.",
        "code": """import datetime


def days_between(start, end):
    if start > end:
        return []
    n = (end - start).days
    return [start + datetime.timedelta(days=i) for i in range(n)]
""",
        "bug_lines": [8],
        "bug": "range(n) omits end (it should use n+1)",
    },
    {
        "id": "merge",
        "spec": "merged(base, override): Return a new dict combining both dicts, with override taking precedence. Never modify either input dict.",
        "code": """def merged(base, override):
    result = base
    for key, value in override.items():
        result[key] = value
    return result
""",
        "bug_lines": [2],
        "bug": "result aliases base and modifies the input (a copy is needed)",
    },
    {
        "id": "retry",
        "spec": "call_with_retry(fn, attempts): Call fn() at most attempts times. Return its value on success; if every attempt raises, re-raise the last exception. attempts >= 1.",
        "code": """def call_with_retry(fn, attempts):
    last = None
    for _ in range(attempts - 1):
        try:
            return fn()
        except Exception as err:
            last = err
    raise last
""",
        "bug_lines": [3],
        "bug": "range(attempts - 1) makes one fewer attempt; with attempts=1 it raises None without calling fn",
    },
    {
        "id": "query",
        "spec": "find_user_sql(name): Return SQL to find a user by name and a tuple of parameters. User input must not enter SQL syntax (use sqlite3 '?' placeholders).",
        "code": """def find_user_sql(name):
    sql = "SELECT id, name FROM users WHERE name = '%s'" % name
    return sql, ()
""",
        "bug_lines": [2],
        "bug": "String interpolation allows SQL injection",
    },
    {
        "id": "ratelimit",
        "spec": "Limiter(n).allow(): Return True or False on each call. Return True only for the first n calls, then False.",
        "code": """class Limiter:
    def __init__(self, n):
        self.n = n
        self.used = 0

    def allow(self):
        if self.used > self.n:
            return False
        self.used += 1
        return True
""",
        "bug_lines": [7],
        "bug": "The condition should use >= instead of >; it allows n+1 calls",
    },
    {
        "id": "duration",
        "spec": "parse_duration(s): Convert '2h', '45m', or '1h30m' to minutes (int). Raise ValueError for invalid formats.",
        "code": """import re


def parse_duration(s):
    m = re.fullmatch(r"(?:(\\d+)h)?(?:(\\d+)m)?", s)
    if not m or not s:
        raise ValueError(s)
    hours, minutes = m.groups()
    if hours:
        return int(hours) * 60
    return int(minutes or 0)
""",
        "bug_lines": [9, 10, 11],
        "bug": "When hours are present, minutes are discarded ('1h30m' → 60)",
    },
    {
        "id": "lru",
        "spec": "LRU(cap): get(k) returns a value or None; put(k, v) stores a value. Evict the least recently used key when capacity is exceeded. A get counts as use.",
        "code": """from collections import OrderedDict


class LRU:
    def __init__(self, cap):
        self.cap = cap
        self.data = OrderedDict()

    def get(self, k):
        if k not in self.data:
            return None
        self.data.move_to_end(k)
        return self.data[k]

    def put(self, k, v):
        self.data[k] = v
        self.data.move_to_end(k)
        if len(self.data) > self.cap:
            self.data.popitem(last=True)
""",
        "bug_lines": [19],
        "bug": "popitem(last=True) evicts the newest key just inserted (it should use last=False)",
    },
    {
        "id": "percent",
        "spec": "share(part, total): Return part/total as a percentage float rounded to two decimal places. Return 0.0 if total is zero.",
        "code": """def share(part, total):
    if total == 0:
        return 0.0
    return round(part * 100 // total, 2)
""",
        "bug_lines": [4],
        "bug": "// performs integer division and loses the fractional part",
    },
    {
        "id": "safepath",
        "spec": "safe_join(root, rel): Return an absolute path under root. Raise ValueError if the result escapes root (through ../ or an absolute path).",
        "code": """import os


def safe_join(root, rel):
    if rel.startswith("/"):
        raise ValueError(rel)
    path = os.path.join(root, rel)
    return os.path.abspath(path)
""",
        "bug_lines": [7, 8],
        "bug": "A ../ path can escape root because it is not blocked (normalize and check the root prefix)",
    },
    {
        "id": "counter",
        "spec": "Counter: The value must remain correct when multiple threads call incr() concurrently. value() returns the current value.",
        "code": """import threading


class Counter:
    def __init__(self):
        self._n = 0
        self._lock = threading.Lock()

    def incr(self):
        self._n += 1

    def value(self):
        with self._lock:
            return self._n
""",
        "bug_lines": [9, 10],
        "bug": "incr uses += without a lock, causing a race condition",
    },
]

# Each parallel task: three independent functions, each with a spec and hidden test cases.
# cases: list of (args tuple, expected) or (args tuple, "raises:ValueError").
PARALLEL = [
    {"id": "text1", "funcs": [
        {"name": "slugify", "spec": "slugify(s): Convert to lowercase, replace each run of non-alphanumeric characters with one '-', and strip leading and trailing '-'.",
         "cases": [(("Hello, World!",), "hello-world"), (("  --A  b--",), "a-b"), (("",), "")]},
        {"name": "roman_to_int", "spec": "roman_to_int(s): Convert a Roman numeral string to an integer (1–3999).",
         "cases": [(("III",), 3), (("MCMXCIV",), 1994), (("XLII",), 42)]},
        {"name": "flatten", "spec": "flatten(xs): Return a flattened list from arbitrarily nested lists.",
         "cases": [(([1, [2, [3, [4]]], 5],), [1, 2, 3, 4, 5]), (([],), []), (([[[]]],), [])]},
    ]},
    {"id": "text2", "funcs": [
        {"name": "is_palindrome", "spec": "is_palindrome(s): Return True if the alphanumeric characters form a palindrome, ignoring case.",
         "cases": [(("A man, a plan, a canal: Panama",), True), (("race a car",), False), (("",), True)]},
        {"name": "rle_encode", "spec": "rle_encode(s): 'aaab' → 'a3b1'. An empty string becomes ''.",
         "cases": [(("aaab",), "a3b1"), (("abc",), "a1b1c1"), (("",), "")]},
        {"name": "rle_decode", "spec": "rle_decode(s): 'a3b1' → 'aaab'. Counts may have multiple digits ('a12').",
         "cases": [(("a3b1",), "aaab"), (("x12",), "x" * 12), (("",), "")]},
    ]},
    {"id": "text3", "funcs": [
        {"name": "word_count", "spec": "word_count(s): Treat each run of English letters as a word. Return a dict of counts by lowercase word.",
         "cases": [(("The cat, the hat.",), {"the": 2, "cat": 1, "hat": 1}), (("",), {})]},
        {"name": "camel_to_snake", "spec": "camel_to_snake(s): 'parseHTTPResponse' → 'parse_http_response', 'userId' → 'user_id'.",
         "cases": [(("userId",), "user_id"), (("parseHTTPResponse",), "parse_http_response"), (("x",), "x")]},
        {"name": "snake_to_camel", "spec": "snake_to_camel(s): 'user_id' → 'userId'. Ignore consecutive, leading, and trailing underscores.",
         "cases": [(("user_id",), "userId"), (("__a__b_",), "aB"), (("x",), "x")]},
    ]},
    {"id": "list1", "funcs": [
        {"name": "chunk", "spec": "chunk(xs, n): Return a list of chunks of length n; the last may be shorter. Raise ValueError if n <= 0.",
         "cases": [(([1, 2, 3, 4, 5], 2), [[1, 2], [3, 4], [5]]), (([], 3), []), (([1], 0), "raises:ValueError")]},
        {"name": "median", "spec": "median(xs): Return the median as a float. Average the two middle values for an even count. Raise ValueError for an empty list.",
         "cases": [(([3, 1, 2],), 2.0), (([4, 1, 3, 2],), 2.5), (([],), "raises:ValueError")]},
        {"name": "dedupe", "spec": "dedupe(xs): Return a list without duplicates, preserving first occurrence order.",
         "cases": [(([3, 1, 3, 2, 1],), [3, 1, 2]), (([],), [])]},
    ]},
    {"id": "list2", "funcs": [
        {"name": "transpose", "spec": "transpose(m): Transpose a rectangular matrix (list of lists). An empty matrix becomes [].",
         "cases": [(([[1, 2, 3], [4, 5, 6]],), [[1, 4], [2, 5], [3, 6]]), (([],), [])]},
        {"name": "merge_intervals", "spec": "merge_intervals(xs): Merge overlapping or touching [a,b] intervals and return a sorted list.",
         "cases": [(([[1, 3], [2, 6], [8, 10], [10, 12]],), [[1, 6], [8, 12]]), (([],), []), (([[5, 6], [1, 2]],), [[1, 2], [5, 6]])]},
        {"name": "group_anagrams", "spec": "group_anagrams(ws): Group anagrams. Sort each group and the list of groups.",
         "cases": [((["eat", "tea", "tan", "ate", "nat", "bat"],), [["ate", "eat", "tea"], ["bat"], ["nat", "tan"]])]},
    ]},
    {"id": "num1", "funcs": [
        {"name": "int_to_roman", "spec": "int_to_roman(n): Convert an integer from 1–3999 to a Roman numeral. Raise ValueError outside this range.",
         "cases": [((1994,), "MCMXCIV"), ((4,), "IV"), ((0,), "raises:ValueError")]},
        {"name": "luhn_valid", "spec": "luhn_valid(s): Return True if a digit string (spaces allowed) passes the Luhn check. Return False for fewer than two digits.",
         "cases": [(("4539 3195 0343 6467",), True), (("8273 1232 7352 0569",), False), (("0",), False)]},
        {"name": "digital_root", "spec": "digital_root(n): Repeatedly sum the digits of a nonnegative integer until one digit remains.",
         "cases": [((16,), 7), ((942,), 6), ((0,), 0)]},
    ]},
    {"id": "str1", "funcs": [
        {"name": "caesar", "spec": "caesar(s, k): Rotate English letters by k, preserving case; leave other characters unchanged. k may be negative.",
         "cases": [(("Abc, xyz!", 3), "Def, abc!"), (("Def", -3), "Abc")]},
        {"name": "title_case", "spec": "title_case(s): Capitalize the first letter of each word and lowercase the rest. Keep a, an, the, of, and in lowercase except as the first word.",
         "cases": [(("the lord OF the rings",), "The Lord of the Rings"), (("",), "")]},
        {"name": "balanced", "spec": "balanced(s): Return True if ()[]{} brackets are balanced. Ignore other characters.",
         "cases": [(("{[()]}x",), True), (("([)]",), False), (("(",), False)]},
    ]},
    {"id": "misc1", "funcs": [
        {"name": "fizzbuzz", "spec": "fizzbuzz(n): Return strings for 1..n. Multiples of 3 become Fizz, multiples of 5 Buzz, and multiples of both FizzBuzz.",
         "cases": [((5,), ["1", "2", "Fizz", "4", "Buzz"]), ((0,), [])]},
        {"name": "hamming", "spec": "hamming(a, b): Count positions where equal-length strings differ. Raise ValueError if lengths differ.",
         "cases": [(("karolin", "kathrin"), 3), (("", ""), 0), (("a", "ab"), "raises:ValueError")]},
        {"name": "parse_kv", "spec": "parse_kv(s): 'a=1;b=2' → {'a': '1', 'b': '2'}. Ignore empty parts and parts without '='; strip whitespace from both ends.",
         "cases": [(("a=1; b = 2;;c",), {"a": "1", "b": "2"}), (("",), {})]},
    ]},
]

assert len(REVIEW) + len(PARALLEL) == 20
