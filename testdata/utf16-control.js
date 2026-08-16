// Generate the control corpus for utf16_control_test.go.
//
//	node testdata/utf16-control.js > testdata/utf16-control.json
//
// JavaScript is not a convenient way to describe test cases; it is the
// definition of the thing being tested. A JavaScript string *is* a sequence of
// UTF-16 code units, `length` counts them, and `slice` cuts between them, so
// what this file records is not an opinion about what UTF-16 addressing should
// do — it is what the environment the offsets come from actually does. Checking
// our answers against answers we computed ourselves would prove nothing here.
//
// Everything below is written with \u escapes rather than literal characters,
// so that the corpus cannot be changed by an editor, a terminal or a patch
// normalising something on the way past.

const docs = [
  { name: "empty", text: "" },
  { name: "ascii", text: "the quick brown fox" },
  // Multi-byte in UTF-8, one code unit in UTF-16: the case where the two
  // encodings disagree about bytes and agree about offsets.
  { name: "latin", text: "élan vital, garçon" },
  { name: "greek-cyrillic", text: "αβγ да" },
  // Combining marks: several code points, one grapheme, all in the BMP. Rune
  // and UTF-16 offsets agree, and a caller who confuses either with "character"
  // as a reader means it is wrong in a third way this package does not address.
  { name: "combining", text: "égal à côte" },
  { name: "cjk-bmp", text: "日本語のテキスト" },
  // CJK extension B: supplementary, and the reason "CJK is two bytes" is wrong.
  { name: "cjk-ext-b", text: "\u{20BB7}野家 \u{2A6B2} \u{2B81D}" },
  { name: "emoji", text: "a \u{1F600} b \u{1F389} c" },
  // A ZWJ sequence: seven code points, eleven code units, one glyph.
  { name: "emoji-zwj", text: "\u{1F469}‍\u{1F469}‍\u{1F467}‍\u{1F466} family" },
  // Regional indicators: every code point supplementary.
  { name: "flags", text: "\u{1F1EB}\u{1F1F7} \u{1F1EF}\u{1F1F5}" },
  // Mathematics, which is what the intended consumer is full of: the operators
  // are BMP, the double-struck and script alphabets are not.
  { name: "math", text: "∀x ∈ ℝ, \u{1D538} ⊂ \u{1D54F}" },
  { name: "math-script", text: "\u{1D4AE}(\u{1D4B0}) = ∫\u{1D453}" },
  { name: "leading-astral", text: "\u{1F600}abc" },
  { name: "trailing-astral", text: "abc\u{1F600}" },
  { name: "only-astral", text: "\u{1F600}\u{1F4A9}\u{1F680}" },
  { name: "alternating", text: "x\u{1D11E}y\u{1F600}z日本" },
  // A supplementary character at both ends of the document, so that every
  // boundary case has a pair beside it.
  { name: "astral-both-ends", text: "\u{1F600}mid\u{1F680}" },
];

const inserts = ["", "X", "\u{1F984}"];
const deleteLengths = [0, 1, 2, 3, 4];

// splitsAt reports whether offset p falls between the two code units of one
// character, which is possible only strictly inside the string.
function splitsAt(s, p) {
  if (p <= 0 || p >= s.length) return false;
  const hi = s.charCodeAt(p - 1);
  const lo = s.charCodeAt(p);
  return hi >= 0xd800 && hi <= 0xdbff && lo >= 0xdc00 && lo <= 0xdfff;
}

// units renders a string as its code units, which is the only lossless way to
// record one that JSON cannot carry — a string holding half a surrogate pair is
// not text and has no UTF-8 encoding.
function units(s) {
  return Array.from({ length: s.length }, (_, i) => s.charCodeAt(i));
}

const out = { generator: `node ${process.version}`, docs: [] };

for (const { name, text } of docs) {
  const entry = {
    name,
    text,
    utf16Len: text.length,
    runeLen: [...text].length,
    offsets: [],
    inserts: [],
    deletes: [],
    damaged: [],
  };

  for (let p = 0; p <= text.length; p++) {
    const split = splitsAt(text, p);
    // [...s] iterates code points, so its length is the rune offset. At an
    // offset that splits a pair it counts the lone surrogate left behind as a
    // code point of its own, which is exactly the corruption being guarded
    // against, so no rune offset is recorded for those.
    entry.offsets.push(
      split ? { u16: p, split: true } : { u16: p, split: false, rune: [...text.slice(0, p)].length },
    );

    if (split) {
      // What JavaScript itself produces when an offset splits a character. It is
      // recorded so the Go test can assert that the control instrument's own
      // answer here is a broken string rather than a plausible one.
      entry.damaged.push({ at: p, units: units(text.slice(0, p) + "X" + text.slice(p)) });
      continue;
    }

    for (const ins of inserts) {
      const want = text.slice(0, p) + ins + text.slice(p);
      entry.inserts.push({ at: p, text: ins, want, wantLen16: want.length });
    }
    for (const n of deleteLengths) {
      if (p + n > text.length || splitsAt(text, p + n)) continue;
      const want = text.slice(0, p) + text.slice(p + n);
      entry.deletes.push({ at: p, len: n, want, wantLen16: want.length });
    }
  }

  out.docs.push(entry);
}

// One record per line: the corpus is meant to be read and reviewed in a diff,
// and JSON.stringify's own indentation spreads a four-field record over twenty
// lines.
const j = (v) => JSON.stringify(v);
let text = `{\n "generator": ${j(out.generator)},\n "docs": [\n`;
text += out.docs
  .map((d) => {
    let t = `  {\n   "name": ${j(d.name)},\n   "text": ${j(d.text)},\n`;
    t += `   "utf16Len": ${d.utf16Len},\n   "runeLen": ${d.runeLen},\n`;
    const keys = ["offsets", "inserts", "deletes", "damaged"];
    for (const key of keys) {
      const rows = d[key].map((r) => `    ${j(r)}`).join(",\n");
      t += `   ${j(key)}: [\n${rows}${rows ? "\n" : ""}   ]`;
      t += key === keys[keys.length - 1] ? "\n" : ",\n";
    }
    return t + "  }";
  })
  .join(",\n");
process.stdout.write(text + "\n ]\n}\n");
