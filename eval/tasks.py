"""P7 regression eval set (design §15 P7): 20 representative tasks.

review   (12): seeded-bug Python modules. Same-family vs cross-family reviewers must report the
               planted defect; detection is judged by the reported line numbers (BUG_LINES).
parallel  (8): three independent functions per task. One worker vs three parallel workers;
               judged by hidden unit tests and wall time.
"""

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
