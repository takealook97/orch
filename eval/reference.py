"""Reference implementations used only by `run.py selfcheck` to validate the hidden tests."""
import re


def slugify(s):
    return re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")


def roman_to_int(s):
    v = {"I": 1, "V": 5, "X": 10, "L": 50, "C": 100, "D": 500, "M": 1000}
    total = 0
    for i, ch in enumerate(s):
        if i + 1 < len(s) and v[ch] < v[s[i + 1]]:
            total -= v[ch]
        else:
            total += v[ch]
    return total


def flatten(xs):
    out = []
    for x in xs:
        out.extend(flatten(x) if isinstance(x, list) else [x])
    return out


def is_palindrome(s):
    t = [c.lower() for c in s if c.isalnum()]
    return t == t[::-1]


def rle_encode(s):
    return "".join(f"{m.group(1)}{len(m.group(0))}" for m in re.finditer(r"(.)\1*", s))


def rle_decode(s):
    return "".join(ch * int(n) for ch, n in re.findall(r"(\D)(\d+)", s))


def word_count(s):
    out = {}
    for w in re.findall(r"[A-Za-z]+", s):
        out[w.lower()] = out.get(w.lower(), 0) + 1
    return out


def camel_to_snake(s):
    s = re.sub(r"([A-Z]+)([A-Z][a-z])", r"\1_\2", s)
    return re.sub(r"([a-z0-9])([A-Z])", r"\1_\2", s).lower()


def snake_to_camel(s):
    parts = [p for p in s.split("_") if p]
    return parts[0] + "".join(p.capitalize() for p in parts[1:]) if parts else ""


def chunk(xs, n):
    if n <= 0:
        raise ValueError(n)
    return [xs[i:i + n] for i in range(0, len(xs), n)]


def median(xs):
    if not xs:
        raise ValueError("empty")
    s = sorted(xs)
    m = len(s) // 2
    return float(s[m]) if len(s) % 2 else (s[m - 1] + s[m]) / 2


def dedupe(xs):
    seen, out = set(), []
    for x in xs:
        if x not in seen:
            seen.add(x)
            out.append(x)
    return out


def transpose(m):
    return [list(r) for r in zip(*m)] if m else []


def merge_intervals(xs):
    out = []
    for a, b in sorted(xs):
        if out and a <= out[-1][1]:
            out[-1][1] = max(out[-1][1], b)
        else:
            out.append([a, b])
    return out


def group_anagrams(ws):
    groups = {}
    for w in ws:
        groups.setdefault("".join(sorted(w)), []).append(w)
    return sorted(sorted(g) for g in groups.values())


def int_to_roman(n):
    if not 1 <= n <= 3999:
        raise ValueError(n)
    table = [(1000, "M"), (900, "CM"), (500, "D"), (400, "CD"), (100, "C"), (90, "XC"),
             (50, "L"), (40, "XL"), (10, "X"), (9, "IX"), (5, "V"), (4, "IV"), (1, "I")]
    out = ""
    for v, r in table:
        while n >= v:
            out += r
            n -= v
    return out


def luhn_valid(s):
    digits = [int(c) for c in s if c.isdigit()]
    if len(digits) < 2 or any(not (c.isdigit() or c == " ") for c in s):
        return False
    total = 0
    for i, d in enumerate(reversed(digits)):
        if i % 2:
            d = d * 2 - 9 if d * 2 > 9 else d * 2
        total += d
    return total % 10 == 0


def digital_root(n):
    return 0 if n == 0 else 1 + (n - 1) % 9


def caesar(s, k):
    def shift(c):
        if c.isalpha() and c.isascii():
            base = ord("A") if c.isupper() else ord("a")
            return chr((ord(c) - base + k) % 26 + base)
        return c
    return "".join(shift(c) for c in s)


def title_case(s):
    small = {"a", "an", "the", "of", "in"}
    words = s.split()
    return " ".join(w.lower() if i and w.lower() in small else w[:1].upper() + w[1:].lower()
                    for i, w in enumerate(words))


def balanced(s):
    pairs, stack = {")": "(", "]": "[", "}": "{"}, []
    for c in s:
        if c in "([{":
            stack.append(c)
        elif c in pairs:
            if not stack or stack.pop() != pairs[c]:
                return False
    return not stack


def fizzbuzz(n):
    return ["FizzBuzz" if i % 15 == 0 else "Fizz" if i % 3 == 0 else "Buzz" if i % 5 == 0 else str(i)
            for i in range(1, n + 1)]


def hamming(a, b):
    if len(a) != len(b):
        raise ValueError("length")
    return sum(x != y for x, y in zip(a, b))


def parse_kv(s):
    out = {}
    for part in s.split(";"):
        if "=" in part:
            k, v = part.split("=", 1)
            if k.strip():
                out[k.strip()] = v.strip()
    return out
