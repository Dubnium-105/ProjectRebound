import fs from "node:fs";
import path from "node:path";
import ts from "typescript";

const sourceRoot = path.resolve("src");
const excluded = new Set([
  "i18n.tsx",
  "api/openapi.generated.ts",
  "api/client.test.ts",
  "auth/provider.test.ts",
  "test/setup.ts"
]);

const files = [];
walk(sourceRoot);

for (const file of files) {
  const relative = path.relative(sourceRoot, file).replaceAll("\\", "/");
  if (excluded.has(relative) || relative.endsWith(".test.ts") || relative.endsWith(".test.tsx")) {
    continue;
  }
  const source = fs.readFileSync(file, "utf8");
  if (!hasHan(source)) continue;

  const sourceFile = ts.createSourceFile(
    file,
    source,
    ts.ScriptTarget.Latest,
    true,
    file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  );
  let changed = false;

  const transformer = (context) => {
    const factory = context.factory;
    const visit = (node) => {
      if (ts.isJsxAttribute(node) && node.initializer && ts.isStringLiteral(node.initializer) && hasHan(node.initializer.text)) {
        changed = true;
        return factory.updateJsxAttribute(
          node,
          node.name,
          factory.createJsxExpression(
            undefined,
            factory.createCallExpression(factory.createIdentifier("tr"), undefined, [
              factory.createStringLiteral(node.initializer.text)
            ])
          )
        );
      }
      if (ts.isJsxText(node) && hasHan(node.text)) {
        const text = node.text.trim();
        if (!text) return node;
        changed = true;
        return factory.createJsxExpression(
          undefined,
          factory.createCallExpression(factory.createIdentifier("tr"), undefined, [
            factory.createStringLiteral(text)
          ])
        );
      }
      if (
        (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) &&
        hasHan(node.text) &&
        !isImportLike(node) &&
        !isPropertyName(node)
      ) {
        changed = true;
        return factory.createCallExpression(factory.createIdentifier("tr"), undefined, [
          factory.createStringLiteral(node.text)
        ]);
      }
      if (ts.isTemplateExpression(node) && node.templateSpans.some((span) => hasHan(span.literal.text)) || ts.isTemplateExpression(node) && hasHan(node.head.text)) {
        changed = true;
        return factory.createCallExpression(factory.createIdentifier("tr"), undefined, [
          ts.visitEachChild(node, visit, context)
        ]);
      }
      return ts.visitEachChild(node, visit, context);
    };
    return (root) => ts.visitNode(root, visit);
  };

  const result = ts.transform(sourceFile, [transformer]);
  let transformed = result.transformed[0];
  result.dispose();
  if (!changed) continue;

  const importPath = relative.includes("/") ? "../".repeat(relative.split("/").length - 1) + "i18n" : "./i18n";
  const trImport = ts.factory.createImportDeclaration(
    undefined,
    ts.factory.createImportClause(
      false,
      undefined,
      ts.factory.createNamedImports([
        ts.factory.createImportSpecifier(false, undefined, ts.factory.createIdentifier("tr"))
      ])
    ),
    ts.factory.createStringLiteral(importPath),
    undefined
  );
  transformed = ts.factory.updateSourceFile(transformed, [trImport, ...transformed.statements]);
  const printed = ts.createPrinter({newLine: ts.NewLineKind.LineFeed}).printFile(transformed);
  fs.writeFileSync(file, printed, "utf8");
}

function walk(directory) {
  for (const entry of fs.readdirSync(directory, {withFileTypes: true})) {
    const fullPath = path.join(directory, entry.name);
    if (entry.isDirectory()) walk(fullPath);
    else if (entry.isFile() && (entry.name.endsWith(".ts") || entry.name.endsWith(".tsx"))) files.push(fullPath);
  }
}

function hasHan(value) {
  return /[\p{Script=Han}]/u.test(value);
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
