import fs from "node:fs";
import path from "node:path";
import ts from "typescript";

const values = new Set();
const sourceRoot = path.resolve("src");
walk(sourceRoot);
const output = [...values].sort((a, b) => a.localeCompare(b, "zh-CN")).join("\n");
if (process.argv[2]) {
  fs.writeFileSync(path.resolve(process.argv[2]), output, "utf8");
} else {
  process.stdout.write(output);
}

function walk(directory) {
  for (const entry of fs.readdirSync(directory, {withFileTypes: true})) {
    const fullPath = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      walk(fullPath);
      continue;
    }
    if (
      !entry.isFile() ||
      (!entry.name.endsWith(".ts") && !entry.name.endsWith(".tsx")) ||
      entry.name.includes(".test.") ||
      entry.name === "i18n.generated.ts"
    ) {
      continue;
    }
    const source = fs.readFileSync(fullPath, "utf8");
    const sourceFile = ts.createSourceFile(
      fullPath,
      source,
      ts.ScriptTarget.Latest,
      true,
      fullPath.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS
    );
    visit(sourceFile);
  }
}

function visit(node) {
  if ((ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || ts.isJsxText(node)) && hasHan(node.text)) {
    const text = node.text.trim();
    if (text) values.add(text);
  } else if (ts.isTemplateExpression(node)) {
    if (hasHan(node.head.text)) values.add(node.head.text.trim());
    for (const span of node.templateSpans) {
      if (hasHan(span.literal.text)) values.add(span.literal.text.trim());
    }
  }
  ts.forEachChild(node, visit);
}

function hasHan(value) {
  return /[\p{Script=Han}]/u.test(value);
}
