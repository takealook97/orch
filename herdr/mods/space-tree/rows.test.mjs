import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const rows = readFileSync(new URL("./rows.js", import.meta.url), "utf8");

test("compact rows use one-line pane details and aligned indentation", () => {
  assert.match(rows, /\.h\(row\.kind === "space" && !row\.data\.parentId \? 26 : 24\)/);
  assert.match(rows, /\.pl\(2 \+ row\.depth \* 12\)/);
  assert.match(rows, /\.left\(2 \+ row\.depth \* 12\)/);
  assert.match(rows, /\.gap\(row\.kind === "pane" \? 6 : 5\)/);
  assert.match(rows, /h_flex\(\)\.flex_none\(\)\.min_w\(0\)\.max_w_3_5\(\)\.text_size\(12\)\.text_color\(t\.foreground\)\.child\(name\)/);
  assert.match(rows, /div\(\)\.flex_1\(\)\.min_w\(0\)\.truncate\(\)\.text_size\(11\)\.text_color\(t\.dim\)\.child\(row\.sub\)/);
  assert.doesNotMatch(rows, /v_flex/);
});
