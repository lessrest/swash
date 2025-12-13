#!/usr/bin/env bun

import { Glob, $ } from "bun";

// Types based on rustdoc JSON format v56+

interface RustdocJson {
  root: number;
  crate_version: string | null;
  includes_private: boolean;
  format_version: number;
  index: Record<string, Item>;
  paths: Record<string, ItemSummary>;
  external_crates: Record<string, ExternalCrate>;
}

interface Item {
  id: number;
  crate_id: number;
  name: string | null;
  span: Span | null;
  visibility: string;
  docs: string | null;
  links: Record<string, number>;
  attrs: unknown[];
  deprecation: Deprecation | null;
  inner: ItemInner;
}

interface ItemInner {
  module?: ModuleInner;
  struct?: StructInner;
  enum?: EnumInner;
  function?: FunctionInner;
  type_alias?: TypeAliasInner;
  use?: UseInner;
  impl?: ImplInner;
  variant?: VariantInner;
  struct_field?: unknown;
  assoc_type?: unknown;
  trait?: TraitInner;
}

interface ModuleInner {
  is_crate: boolean;
  items: number[];
  is_stripped: boolean;
}

interface StructInner {
  kind: unknown;
  generics: Generics;
  impls: number[];
}

interface EnumInner {
  generics: Generics;
  variants: number[];
  impls: number[];
}

interface TraitInner {
  is_auto: boolean;
  is_unsafe: boolean;
  is_dyn_compatible: boolean;
  items: number[];
  generics: Generics;
  bounds: unknown[];
}

interface FunctionInner {
  sig: FnDecl;
  generics: Generics;
  header: FunctionHeader;
  has_body: boolean;
}

interface FunctionHeader {
  is_const: boolean;
  is_unsafe: boolean;
  is_async: boolean;
  abi: string;
}

interface FnDecl {
  inputs: [string, unknown][];
  output: unknown | null;
  is_c_variadic: boolean;
}

interface TypeAliasInner {
  type: unknown;
  generics: Generics;
}

interface UseInner {
  source: string;
  name: string;
  id: number | null;
  is_glob: boolean;
}

interface ImplInner {
  is_unsafe: boolean;
  generics: Generics;
  provided_trait_methods: string[];
  trait: unknown | null;
  for: unknown;
  items: number[];
  is_negative: boolean;
  is_synthetic: boolean;
  blanket_impl?: string;
}

interface VariantInner {
  kind: unknown;
  discriminant: unknown | null;
}

interface Generics {
  params: GenericParamDef[];
  where_predicates: unknown[];
}

interface GenericParamDef {
  name: string;
  kind: unknown;
}

interface Span {
  filename: string;
  begin: [number, number];
  end: [number, number];
}

interface Deprecation {
  since: string | null;
  note: string | null;
}

interface ItemSummary {
  crate_id: number;
  path: string[];
  kind: string;
}

interface ExternalCrate {
  name: string;
  html_root_url: string | null;
}

// Combined crate database for cross-crate resolution
interface CrateDb {
  crates: Map<string, RustdocJson>;  // crate name -> json
  // For each crate, map external crate_id -> crate name
  crateIdMaps: Map<string, Map<number, string>>;
}

function buildCrateDb(jsonFiles: RustdocJson[]): CrateDb {
  const db: CrateDb = {
    crates: new Map(),
    crateIdMaps: new Map(),
  };
  
  for (const json of jsonFiles) {
    const rootItem = json.index[json.root.toString()];
    const crateName = rootItem?.name || "unknown";
    db.crates.set(crateName, json);
    
    // Build crate_id -> name map from external_crates
    const idMap = new Map<number, string>();
    idMap.set(0, crateName); // crate_id 0 is always self
    for (const [id, ext] of Object.entries(json.external_crates)) {
      idMap.set(parseInt(id), ext.name);
    }
    db.crateIdMaps.set(crateName, idMap);
  }
  
  return db;
}

// Resolve an item ID, potentially across crates
function resolveItem(db: CrateDb, fromCrate: string, itemId: number): { item: Item; crateName: string } | null {
  const json = db.crates.get(fromCrate);
  if (!json) return null;
  
  const item = json.index[itemId.toString()];
  if (item) {
    // Check if it's a local item or from another crate
    if (item.crate_id === 0) {
      return { item, crateName: fromCrate };
    }
    // It's from an external crate - try to find it
    const idMap = db.crateIdMaps.get(fromCrate);
    const extCrateName = idMap?.get(item.crate_id);
    if (extCrateName && db.crates.has(extCrateName)) {
      // Look up in the external crate's index
      const extJson = db.crates.get(extCrateName)!;
      const extItem = extJson.index[itemId.toString()];
      if (extItem) {
        return { item: extItem, crateName: extCrateName };
      }
    }
  }
  
  // Try looking up in paths and finding the crate
  const pathInfo = json.paths[itemId.toString()];
  if (pathInfo) {
    const idMap = db.crateIdMaps.get(fromCrate);
    const extCrateName = idMap?.get(pathInfo.crate_id);
    if (extCrateName && db.crates.has(extCrateName)) {
      // Search by path in the external crate
      const extJson = db.crates.get(extCrateName)!;
      for (const [id, extItem] of Object.entries(extJson.index)) {
        const extPath = extJson.paths[id];
        if (extPath && arraysEqual(extPath.path, pathInfo.path)) {
          return { item: extItem, crateName: extCrateName };
        }
      }
    }
  }
  
  return null;
}

function arraysEqual(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

// Helper to get the kind of an item from its inner field
function getItemKind(item: Item): string {
  const keys = Object.keys(item.inner);
  return keys[0] || "unknown";
}

// Format a brief doc summary (first line/sentence)
function formatDocSummary(docs: string | null, maxLen = 80): string {
  if (!docs) return "";
  // Get first line or first sentence
  const firstLine = docs.split("\n")[0].trim();
  const firstSentence = firstLine.split(". ")[0];
  const summary = firstSentence.length < firstLine.length ? firstSentence + "." : firstLine;
  if (summary.length > maxLen) {
    return summary.slice(0, maxLen - 3) + "...";
  }
  return summary;
}

// Get docs for a re-exported item by resolving across crates
function getReexportDocs(db: CrateDb, fromCrate: string, useInner: UseInner): string | null {
  if (useInner.id === null) return null;
  
  // First try to resolve in the source crate based on the path
  const sourceCrate = useInner.source.split("::")[0];
  if (db.crates.has(sourceCrate)) {
    const json = db.crates.get(sourceCrate)!;
    // Search by path
    const targetPath = useInner.source.split("::");
    for (const [id, item] of Object.entries(json.index)) {
      const pathInfo = json.paths[id];
      if (pathInfo && arraysEqual(pathInfo.path, targetPath)) {
        return item.docs;
      }
    }
    // Also try with crate name prepended
    for (const [id, item] of Object.entries(json.index)) {
      const pathInfo = json.paths[id];
      if (pathInfo && item.name === useInner.name && item.docs) {
        return item.docs;
      }
    }
  }
  
  return null;
}

// Wrap text to fit within maxWidth, with a prefix for continuation lines
function wrapList(items: string[], prefix: string, maxWidth = 80): string[] {
  const lines: string[] = [];
  let current = prefix;
  
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    const sep = i < items.length - 1 ? ", " : "";
    const addition = item + sep;
    
    if (current.length + addition.length > maxWidth && current !== prefix) {
      lines.push(current);
      current = " ".repeat(prefix.length) + addition;
    } else {
      current += addition;
    }
  }
  if (current.trim()) lines.push(current);
  return lines;
}

// Format span as short path
function formatSpan(span: Span | null): string {
  if (!span) return "";
  // Extract just the relevant part of the path
  const parts = span.filename.split("/");
  const srcIdx = parts.findIndex(p => p === "src");
  const shortPath = srcIdx >= 0 ? parts.slice(srcIdx).join("/") : parts.slice(-2).join("/");
  return `${shortPath}:${span.begin[0]}`;
}

// Print module overview recursively
function printModule(
  db: CrateDb,
  crateName: string,
  itemId: number, 
  indent = 0,
  seen = new Set<string>()
): void {
  const key = `${crateName}:${itemId}`;
  if (seen.has(key)) return;
  seen.add(key);
  
  const json = db.crates.get(crateName);
  if (!json) return;
  
  const item = json.index[itemId.toString()];
  if (!item) return;
  
  const kind = getItemKind(item);
  const prefix = "  ".repeat(indent);
  
  if (kind === "module") {
    const mod = item.inner.module!;
    const name = item.name || "(root)";
    const crateLabel = mod.is_crate ? ` (${crateName})` : "";
    const loc = item.span ? ` [${formatSpan(item.span)}]` : "";
    
    console.log(`${prefix}mod ${name}${crateLabel}${loc}`);
    
    // Group items by kind
    const grouped: Record<string, Item[]> = {};
    for (const childId of mod.items) {
      const child = json.index[childId.toString()];
      if (!child) continue;
      const childKind = getItemKind(child);
      if (!grouped[childKind]) grouped[childKind] = [];
      grouped[childKind].push(child);
    }
    
    // Print submodules first
    if (grouped["module"]) {
      for (const submod of grouped["module"]) {
        printModule(db, crateName, submod.id, indent + 1, seen);
      }
    }
    
    // Print other public items as inline lists
    const publicKinds = ["struct", "enum", "trait", "function", "type_alias"];
    for (const k of publicKinds) {
      if (grouped[k] && grouped[k].length > 0) {
        const items = grouped[k].filter(i => i.visibility === "public" && i.name);
        if (items.length > 0) {
          const kindLabel = k === "type_alias" ? "types" : k + "s";
          const names = items.map(i => i.name!);
          const linePrefix = `${prefix}  ${kindLabel}: `;
          for (const line of wrapList(names, linePrefix)) {
            console.log(line);
          }
        }
      }
    }
    
    // Re-exports as inline list
    if (grouped["use"]) {
      const reexports = grouped["use"].filter(i => i.visibility === "public");
      if (reexports.length > 0) {
        const names = reexports.map(i => i.inner.use!.name);
        const linePrefix = `${prefix}  re-exports: `;
        for (const line of wrapList(names, linePrefix)) {
          console.log(line);
        }
      }
    }
  }
}

// Print crate statistics
function printStats(db: CrateDb): void {
  const stats: string[] = [];
  let totalItems = 0;
  
  for (const [name, json] of db.crates) {
    const localItems = Object.values(json.index).filter(i => i.crate_id === 0);
    totalItems += localItems.length;
    stats.push(`${name}:${localItems.length}`);
  }
  
  console.log(`${totalItems} items total (${stats.join(", ")})`);
  console.log();
}

// Find items by name or path across all crates
function findItems(db: CrateDb, query: string): Array<{ item: Item; crateName: string; path: string[]; id: string }> {
  const results: Array<{ item: Item; crateName: string; path: string[]; id: string }> = [];
  const queryParts = query.split("::");
  const isPath = queryParts.length > 1;
  const queryName = queryParts[queryParts.length - 1];
  
  for (const [crateName, json] of db.crates) {
    // Build a map of item id -> impl parent path (for methods)
    const implMethodPaths = new Map<string, string[]>();
    for (const [id, item] of Object.entries(json.index)) {
      if (item.inner.impl) {
        const impl = item.inner.impl;
        // Get the "for" type's path
        const forType = impl.for;
        if (forType?.resolved_path?.path) {
          const parentPath = forType.resolved_path.path.split("::");
          for (const methodId of impl.items) {
            const method = json.index[methodId.toString()];
            if (method?.name) {
              implMethodPaths.set(methodId.toString(), [...parentPath, method.name]);
            }
          }
        }
      }
    }
    
    for (const [id, item] of Object.entries(json.index)) {
      if (item.crate_id !== 0) continue; // only local items
      
      const pathInfo = json.paths[id];
      let itemPath = pathInfo?.path || implMethodPaths.get(id) || (item.name ? [crateName, item.name] : null);
      if (!itemPath) continue;
      
      if (isPath) {
        // Match by path suffix
        if (itemPath.length >= queryParts.length) {
          const suffix = itemPath.slice(-queryParts.length);
          if (suffix.join("::") === query) {
            results.push({ item, crateName, path: itemPath, id });
          }
        }
      } else {
        // Match by name only
        if (item.name === query) {
          results.push({ item, crateName, path: itemPath, id });
        }
      }
    }
  }
  
  return results;
}

// Find item by ID (supports "crate#id" or just "id")
function findItemById(db: CrateDb, query: string): { item: Item; crateName: string; path: string[] } | null {
  let targetCrate: string | null = null;
  let id = query;
  
  if (query.includes("#")) {
    [targetCrate, id] = query.split("#");
  }
  
  for (const [crateName, json] of db.crates) {
    if (targetCrate && crateName !== targetCrate) continue;
    const item = json.index[id];
    if (item && item.crate_id === 0) {
      const pathInfo = json.paths[id];
      return { item, crateName, path: pathInfo?.path || [crateName, item.name || "?"] };
    }
  }
  return null;
}

// Format a type for display
function formatType(ty: any, short = false): string {
  if (!ty) return "?";
  
  if (ty.primitive) return ty.primitive;
  if (ty.generic) return ty.generic;
  
  // Handle both {resolved_path: {...}} and direct {path: ..., args: ...} forms
  const rp = ty.resolved_path || (ty.path ? ty : null);
  if (rp && rp.path) {
    let name = rp.path;
    // Use short name (last component) if requested
    if (short && typeof name === "string" && name.includes("::")) {
      name = name.split("::").pop()!;
    }
    let s = name;
    if (rp.args?.angle_bracketed?.args?.length) {
      const args = rp.args.angle_bracketed.args
        .map((a: any) => a.type ? formatType(a.type, short) : a.lifetime || "?")
        .join(", ");
      s += `<${args}>`;
    }
    return s;
  }
  if (ty.borrowed_ref) {
    const lt = ty.borrowed_ref.lifetime ? `${ty.borrowed_ref.lifetime} ` : "";
    const mut = ty.borrowed_ref.mutable ? "mut " : "";
    return `&${lt}${mut}${formatType(ty.borrowed_ref.type, short)}`;
  }
  if (ty.slice) return `[${formatType(ty.slice, short)}]`;
  if (ty.array) return `[${formatType(ty.array.type, short)}; ${ty.array.len}]`;
  if (ty.tuple) {
    if (ty.tuple.length === 0) return "()";
    return `(${ty.tuple.map((t: any) => formatType(t, short)).join(", ")})`;
  }
  if (ty.raw_pointer) {
    const mut = ty.raw_pointer.mutable ? "mut " : "const ";
    return `*${mut}${formatType(ty.raw_pointer.type, short)}`;
  }
  if (ty.qualified_path) {
    return `<${formatType(ty.qualified_path.self_type, short)} as ${formatType(ty.qualified_path.trait, short)}>::${ty.qualified_path.name}`;
  }
  if (ty.impl_trait) {
    const bounds = ty.impl_trait.map((b: any) => {
      if (b.trait_bound?.trait) {
        return formatType(b.trait_bound.trait, true);
      }
      return "?";
    }).join(" + ");
    return `impl ${bounds}`;
  }
  if (ty.dyn_trait) {
    const bounds = ty.dyn_trait.traits?.map((b: any) => {
      if (b.trait) return formatType(b.trait, true);
      return "?";
    }).join(" + ") || "?";
    return `dyn ${bounds}`;
  }
  
  return "?";
}

// Format function signature
function formatFnSig(fn: FunctionInner): string {
  const params = fn.sig.inputs.map(([name, ty]) => `${name}: ${formatType(ty)}`).join(", ");
  const ret = fn.sig.output ? ` -> ${formatType(fn.sig.output)}` : "";
  const header = [
    fn.header.is_const ? "const " : "",
    fn.header.is_async ? "async " : "",
    fn.header.is_unsafe ? "unsafe " : "",
  ].join("");
  return `${header}fn(${params})${ret}`;
}

// Format generics
function formatGenerics(gen: Generics): string {
  if (gen.params.length === 0) return "";
  const params = gen.params.map(p => {
    if (p.kind === "lifetime") return p.name;
    if (typeof p.kind === "object" && "type" in p.kind) {
      const bounds = (p.kind as any).type.bounds;
      if (bounds?.length) {
        const boundStr = bounds.map((b: any) => 
          b.trait_bound ? formatType(b.trait_bound.trait) : "?"
        ).join(" + ");
        return `${p.name}: ${boundStr}`;
      }
    }
    return p.name;
  }).join(", ");
  return `<${params}>`;
}

// Print detailed item info
function printItemDetail(db: CrateDb, crateName: string, item: Item, path: string[]): void {
  const kind = getItemKind(item);
  const json = db.crates.get(crateName)!;
  
  console.log(`${kind} ${path.join("::")} (${crateName}#${item.id})`);
  if (item.span) {
    console.log(`  ${item.span.filename}:${item.span.begin[0]}-${item.span.end[0]}`);
  }
  console.log();
  
  // Print docs
  if (item.docs) {
    console.log(item.docs);
    console.log();
  }
  
  // Kind-specific details
  if (kind === "struct" && item.inner.struct) {
    const s = item.inner.struct;
    const gen = formatGenerics(s.generics);
    console.log(`struct ${item.name}${gen}`);
    
    // Find fields
    const structKind = s.kind as any;
    if (structKind?.plain?.fields) {
      console.log("\nFields:");
      for (const fieldId of structKind.plain.fields) {
        const field = json.index[fieldId.toString()];
        if (field && field.inner.struct_field) {
          const ty = formatType(field.inner.struct_field);
          console.log(`  ${field.name}: ${ty}`);
        }
      }
    }
    
    // Find methods from impls
    if (s.impls.length > 0) {
      const methods: Array<{ name: string; sig: string; docs: string | null; id: number }> = [];
      const traitImpls: Array<{ trait: string; methods: string[] }> = [];
      
      for (const implId of s.impls) {
        const impl = json.index[implId.toString()];
        if (!impl || !impl.inner.impl) continue;
        const implInner = impl.inner.impl;
        
        // Skip blanket impls and synthetic impls
        if (implInner.blanket_impl || implInner.is_synthetic) continue;
        
        const implMethods: string[] = [];
        for (const methodId of implInner.items) {
          const method = json.index[methodId.toString()];
          if (!method) continue;
          const methodKind = getItemKind(method);
          if (methodKind === "function" && method.inner.function) {
            const sig = formatFnSig(method.inner.function);
            implMethods.push(method.name || "?");
            if (!implInner.trait) {
              methods.push({ name: method.name || "?", sig, docs: method.docs, id: method.id });
            }
          }
        }
        
        if (implInner.trait && implMethods.length > 0) {
          const traitName = formatType(implInner.trait, true);
          traitImpls.push({ trait: traitName, methods: implMethods });
        }
      }
      
      if (methods.length > 0) {
        console.log("\nMethods:");
        for (const m of methods) {
          console.log(`  ${m.name} (${crateName}#${m.id}): ${m.sig}`);
        }
      }
      
      if (traitImpls.length > 0) {
        console.log("\nTrait implementations:");
        for (const ti of traitImpls) {
          console.log(`  ${ti.trait}: ${ti.methods.join(", ")}`);
        }
      }
    }
  }
  
  if (kind === "enum" && item.inner.enum) {
    const e = item.inner.enum;
    const gen = formatGenerics(e.generics);
    console.log(`enum ${item.name}${gen}`);
    
    // Get variants from the enum's variants field or search index
    const variants: string[] = [];
    for (const [id, indexItem] of Object.entries(json.index)) {
      if (indexItem.inner.variant) {
        // Check if this variant belongs to this enum via paths
        const variantPath = json.paths[id];
        if (variantPath && variantPath.path.length >= 2) {
          const parentPath = variantPath.path.slice(0, -1);
          if (parentPath.join("::") === path.join("::")) {
            variants.push(indexItem.name || "?");
          }
        }
      }
    }
    
    if (variants.length > 0) {
      console.log("\nVariants:");
      for (const v of variants) {
        console.log(`  ${v}`);
      }
    }
  }
  
  if (kind === "function" && item.inner.function) {
    console.log(formatFnSig(item.inner.function));
  }
  
  if (kind === "trait" && item.inner.trait) {
    const t = item.inner.trait;
    const gen = formatGenerics(t.generics);
    const markers = [
      t.is_auto ? "auto " : "",
      t.is_unsafe ? "unsafe " : "",
    ].join("");
    console.log(`${markers}trait ${item.name}${gen}`);
    
    // Get trait items
    const methods: string[] = [];
    const assocTypes: string[] = [];
    for (const itemId of t.items) {
      const traitItem = json.index[itemId.toString()];
      if (!traitItem) continue;
      const traitItemKind = getItemKind(traitItem);
      if (traitItemKind === "function") {
        methods.push(traitItem.name || "?");
      } else if (traitItemKind === "assoc_type") {
        assocTypes.push(traitItem.name || "?");
      }
    }
    
    if (assocTypes.length > 0) {
      console.log(`\nAssociated types: ${assocTypes.join(", ")}`);
    }
    if (methods.length > 0) {
      console.log(`\nMethods: ${methods.join(", ")}`);
    }
  }
  
  if (kind === "type_alias" && item.inner.type_alias) {
    const ta = item.inner.type_alias;
    const gen = formatGenerics(ta.generics);
    console.log(`type ${item.name}${gen} = ${formatType(ta.type)}`);
  }
}

function printHelp() {
  console.log(`rustdoc.ts - explore rustdoc JSON

usage: bun rustdoc.ts <workspace> [command] [args]

commands:
  (none)                    overview of all crates
  <name>                    detail for item by name
  <crate#id>                detail for item by id
  <path::to::item>          detail for item by path
  src <name|path|crate#id>  show source code
  gen [crate...]            generate JSON (cargo rustdoc)

examples:
  bun rustdoc.ts ./oxigraph-wasi-ffi
  bun rustdoc.ts ./oxigraph-wasi-ffi Store
  bun rustdoc.ts ./oxigraph-wasi-ffi src QueryResultsParser::for_reader
  bun rustdoc.ts ./oxigraph-wasi-ffi src sparesults#196
  bun rustdoc.ts ./oxigraph-wasi-ffi gen oxigraph sparesults`);
}

// Main
async function main() {
  const args = process.argv.slice(2);
  
  if (args.length === 0 || args[0] === "-h" || args[0] === "--help") {
    printHelp();
    process.exit(0);
  }
  
  // First arg is workspace dir
  const workspaceDir = args[0];
  const docDir = `${workspaceDir}/target/doc`;
  const restArgs = args.slice(1);
  
  // Parse command
  let command: "overview" | "detail" | "src" | "gen" = "overview";
  let itemQuery: string | null = null;
  let genCrates: string[] = [];
  
  let i = 0;
  while (i < restArgs.length) {
    const arg = restArgs[i];
    if (arg === "src") {
      command = "src";
      if (restArgs[i + 1]) {
        itemQuery = restArgs[i + 1];
        i++;
      }
    } else if (arg === "gen") {
      command = "gen";
      genCrates = restArgs.slice(i + 1);
      break;
    } else if (!arg.startsWith("-")) {
      command = "detail";
      itemQuery = arg;
    }
    i++;
  }
  
  // Handle gen command
  if (command === "gen") {
    const crates = genCrates.length > 0 ? genCrates : [
      "oxigraph", "oxrdf", "oxrdfio", "sparesults", "spareval", "spargebra"
    ];
    console.log(`generating rustdoc JSON for: ${crates.join(", ")}`);
    for (const crate of crates) {
      console.log(`  ${crate}...`);
      try {
        await $`cargo rustdoc -p ${crate} --output-format json -Z unstable-options`.cwd(workspaceDir).quiet();
      } catch (e: any) {
        console.error(`    failed: ${e.message || e}`);
      }
    }
    console.log(`done. JSON files in ${docDir}`);
    return;
  }
  
  // Load JSON files
  let filePaths: string[] = [];
  try {
    const glob = new Glob("*.json");
    for await (const file of glob.scan(docDir)) {
      filePaths.push(`${docDir}/${file}`);
    }
  } catch (e) {
    console.error(`could not scan ${docDir} - run 'gen' first?`);
    process.exit(1);
  }
  
  if (filePaths.length === 0) {
    console.error(`no JSON files in ${docDir} - run 'gen' first?`);
    process.exit(1);
  }
  
  // Filter to only the crates we care about
  const relevantCrates = new Set([
    "oxigraph",
    "oxrdf", 
    "oxrdfio",
    "sparesults",
    "spareval",
    "spargebra"
  ]);
  
  const jsonFiles: RustdocJson[] = [];
  const loadedFiles: string[] = [];
  
  for (const filePath of filePaths) {
    try {
      const file = Bun.file(filePath);
      const json: RustdocJson = await file.json();
      const rootItem = json.index[json.root.toString()];
      const crateName = rootItem?.name;
      
      // Skip irrelevant crates
      if (crateName && !relevantCrates.has(crateName)) {
        continue;
      }
      
      jsonFiles.push(json);
      loadedFiles.push(filePath.replace(docDir + "/", ""));
    } catch (e) {
      // skip
    }
  }
  
  if (jsonFiles.length === 0) {
    console.error(`no relevant JSON files in ${docDir}`);
    process.exit(1);
  }
  
  const db = buildCrateDb(jsonFiles);
  
  // Show loaded files for overview
  if (command === "overview") {
    console.log(`loaded: ${loadedFiles.join(", ")}`);
    console.log();
  }
  
  // Handle commands
  const isIdQuery = (q: string) => /^(\w+#)?\d+$/.test(q);
  
  if (command === "src" && itemQuery) {
    const results = isIdQuery(itemQuery)
      ? [findItemById(db, itemQuery)].filter(Boolean) as Array<{ item: Item; crateName: string; path: string[] }>
      : findItems(db, itemQuery);
    
    if (results.length === 0) {
      console.error(`not found: ${itemQuery}`);
      process.exit(1);
    }
    
    for (let i = 0; i < results.length; i++) {
      const { item, crateName, path } = results[i];
      if (i > 0) console.log("\n" + "─".repeat(60) + "\n");
      
      console.log(`${path.join("::")} (${crateName}#${item.id})`);
      
      if (item.span) {
        const { filename, begin, end } = item.span;
        console.log(`${filename}:${begin[0]}-${end[0]}`);
        console.log();
        
        try {
          const file = Bun.file(filename);
          const content = await file.text();
          const lines = content.split("\n");
          const startLine = Math.max(0, begin[0] - 1);
          const endLine = Math.min(lines.length, end[0]);
          const lineNumWidth = String(endLine).length;
          
          for (let ln = startLine; ln < endLine; ln++) {
            const lineNum = String(ln + 1).padStart(lineNumWidth);
            console.log(`${lineNum} │ ${lines[ln]}`);
          }
        } catch (e) {
          console.log(`  (could not read file)`);
        }
      } else {
        console.log(`  (no source span)`);
      }
    }
    return;
  }
  
  if (command === "detail" && itemQuery) {
    const results = isIdQuery(itemQuery)
      ? [findItemById(db, itemQuery)].filter(Boolean) as Array<{ item: Item; crateName: string; path: string[]; id?: string }>
      : findItems(db, itemQuery);
    
    if (results.length === 0) {
      console.error(`not found: ${itemQuery}`);
      process.exit(1);
    }
    
    for (let i = 0; i < results.length; i++) {
      if (i > 0) console.log("\n" + "─".repeat(60) + "\n");
      const { item, crateName, path } = results[i];
      printItemDetail(db, crateName, item, path);
    }
    return;
  }
  
  // Overview mode
  printStats(db);
  
  // Print oxigraph first if present, then others
  const crateOrder = ["oxigraph", "oxrdf", "oxrdfio", "sparesults", "spareval", "spargebra"];
  for (const crateName of crateOrder) {
    if (db.crates.has(crateName)) {
      const json = db.crates.get(crateName)!;
      printModule(db, crateName, json.root);
      console.log();
    }
  }
}

main();
