import fs from "node:fs";
import path from "node:path";
import ts from "typescript";

const sourceRoot = path.resolve("src");
const excluded = new Set([
  "api/openapi.generated.ts",
  "i18n.generated.ts",
  "i18n.tsx",
  "test/setup.ts"
]);
const failures = [];

walk(sourceRoot);
checkCatalog(path.join(sourceRoot, "i18n.generated.ts"), "generatedEnglishCatalog");
checkCatalog(path.join(sourceRoot, "i18n.tsx"), "englishCatalog", new Set(["中文"]));

if (failures.length) {
  process.stderr.write(`${failures.join("\n")}\n`);
  process.exitCode = 1;
} else {
  process.stdout.write(
    "i18n audit passed: no unwrapped Chinese UI copy, module-scope translations, or Chinese English translations.\n"
  );
}

function walk(directory) {
  for (const entry of fs.readdirSync(directory, {withFileTypes: true})) {
    const fullPath = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      walk(fullPath);
      continue;
    }
    if (!entry.isFile() || (!entry.name.endsWith(".ts") && !entry.name.endsWith(".tsx"))) continue;

    const relative = path.relative(sourceRoot, fullPath).replaceAll("\\", "/");
    if (excluded.has(relative) || relative.includes(".test.")) continue;

    const sourceFile = parse(fullPath);
    visit(sourceFile, (node) => {
      if (isModuleScopeLocaleCall(node)) {
        const position = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile));
        failures.push(
          `${relative}:${position.line + 1}: ${node.expression.text}() runs at module scope and will not react to a locale change.`
        );
      }
      if (!isChineseTextNode(node) || !hasHan(node.text.trim())) return;
      if (isPropertyName(node) || isImportLike(node) || isInsideTr(node)) return;
      const position = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile));
      failures.push(`${relative}:${position.line + 1}: Chinese UI text is not wrapped in tr(): ${JSON.stringify(node.text.trim())}`);
    });
  }
}

function checkCatalog(filePath, variableName, allowedValues = new Set()) {
  const sourceFile = parse(filePath);
  visit(sourceFile, (node) => {
    if (!ts.isVariableDeclaration(node) || node.name.getText(sourceFile) !== variableName) return;
    if (!node.initializer || !ts.isObjectLiteralExpression(node.initializer)) return;
    for (const property of node.initializer.properties) {
      if (!ts.isPropertyAssignment(property) || !ts.isStringLiteralLike(property.initializer)) continue;
      const value = property.initializer.text;
      if (!hasHan(value) || allowedValues.has(value)) continue;
      const position = sourceFile.getLineAndCharacterOfPosition(property.initializer.getStart(sourceFile));
      failures.push(
        `${path.basename(filePath)}:${position.line + 1}: English translation still contains Chinese: ${JSON.stringify(value)}`
      );
    }
  });
}

function parse(filePath) {
  return ts.createSourceFile(
    filePath,
    fs.readFileSync(filePath, "utf8"),
    ts.ScriptTarget.Latest,
    true,
    filePath.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  );
}

function visit(node, visitor) {
  visitor(node);
  ts.forEachChild(node, (child) => visit(child, visitor));
}

function isChineseTextNode(node) {
  return ts.isStringLiteralLike(node) || ts.isJsxText(node) || ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node);
}

function isInsideTr(node) {
  for (let current = node.parent; current; current = current.parent) {
    if (
      ts.isCallExpression(current) &&
      ts.isIdentifier(current.expression) &&
      current.expression.text === "tr" &&
      current.arguments.some((argument) => containsNode(argument, node))
    ) {
      return true;
    }
    if (ts.isStatement(current)) return false;
  }
  return false;
}

function isModuleScopeLocaleCall(node) {
  if (
    !ts.isCallExpression(node) ||
    !ts.isIdentifier(node.expression) ||
    !new Set(["tr", "localeTag", "turnstileLanguage", "isEnglish", "localizeSystemText"]).has(node.expression.text)
  ) {
    return false;
  }
  for (let current = node.parent; current; current = current.parent) {
    if (ts.isFunctionLike(current)) return false;
  }
  return true;
}

function containsNode(container, target) {
  return container.pos <= target.pos && container.end >= target.end;
}

function isImportLike(node) {
  return ts.isImportDeclaration(node.parent) || ts.isExportDeclaration(node.parent);
}

function isPropertyName(node) {
  const parent = node.parent;
  return (
    (ts.isPropertyAssignment(parent) || ts.isPropertyDeclaration(parent) || ts.isMethodDeclaration(parent)) &&
    parent.name === node
  );
}

function hasHan(value) {
  return /[\p{Script=Han}]/u.test(value);
}
