import { parse, generate, type CssNode } from "css-tree";
// Preserve layout and typography while keeping CSS resource loads and escapes
// blocked, independently of the per-message remote image permission.
const properties = new Set(
  "color background background-color font font-family font-size font-weight font-style font-variant line-height letter-spacing word-spacing text-align text-decoration text-transform text-indent white-space word-break overflow-wrap vertical-align display width min-width max-width height min-height max-height margin margin-top margin-right margin-bottom margin-left padding padding-top padding-right padding-bottom padding-left border border-top border-right border-bottom border-left border-color border-width border-style border-radius border-collapse border-spacing table-layout list-style-type list-style-position float clear box-sizing font-stretch font-variant-numeric text-decoration-color text-decoration-style text-decoration-thickness text-underline-offset text-overflow text-shadow word-wrap hyphens overflow overflow-x overflow-y opacity box-shadow outline outline-color outline-style outline-width outline-offset border-top-color border-right-color border-bottom-color border-left-color border-top-width border-right-width border-bottom-width border-left-width border-top-style border-right-style border-bottom-style border-left-style border-top-left-radius border-top-right-radius border-bottom-left-radius border-bottom-right-radius background-size background-position background-repeat background-origin background-clip flex flex-basis flex-grow flex-shrink flex-direction flex-wrap flex-flow order justify-content justify-items justify-self align-content align-items align-self place-content place-items place-self gap row-gap column-gap grid grid-template grid-template-columns grid-template-rows grid-template-areas grid-auto-columns grid-auto-rows grid-auto-flow grid-column grid-column-start grid-column-end grid-row grid-row-start grid-row-end grid-area position top right bottom left inset z-index transform transform-origin object-fit object-position aspect-ratio visibility direction unicode-bidi".split(
    " ",
  ),
);
function safeValue(value: string): boolean {
  return (
    /^[\p{L}\p{N}\s#.,%()+/'"!:_\[\]-]+$/u.test(value) &&
    !/url|expression|image|var\(|attr\(/i.test(value)
  );
}
function allowed(name: string, value: string): boolean {
  return (
    properties.has(name.toLowerCase()) &&
    safeValue(value) &&
    (name.toLowerCase() !== "position" ||
      /^(static|relative|absolute|sticky|initial|inherit|unset|revert)$/i.test(
        value.trim(),
      ))
  );
}
function declarations(style: CSSStyleDeclaration): string {
  const result: string[] = [];
  for (let i = 0; i < style.length; i++) {
    const name = style[i];
    const value = style.getPropertyValue(name);
    if (allowed(name, value)) {
      result.push(
        `${name}:${value}${style.getPropertyPriority(name) ? " !important" : ""}`,
      );
    }
  }
  return result.join(";");
}
function cssNodes(node: CssNode, depth = 0): string {
  if (depth > 16) return "";
  if (node.type === "Declaration") {
    const value = generate(node.value!);
    return allowed(node.property || "", value)
      ? `${node.property}:${value}${node.important ? " !important" : ""};`
      : "";
  }
  if (node.type === "Rule") {
    const selector = generate(node.prelude!);
    if (/[\\<>@]/.test(selector)) return "";
    const body = cssNodes(node.block!, depth + 1);
    return body ? `${selector}{${body}}` : "";
  }
  if (node.type === "Atrule") {
    if (
      !/^(media|supports)$/i.test(node.name || "") ||
      !node.prelude ||
      !node.block
    )
      return "";
    const condition = generate(node.prelude);
    return safeValue(condition)
      ? `@${node.name} ${condition}{${cssNodes(node.block, depth + 1)}}`
      : "";
  }
  if (node.type === "StyleSheet" || node.type === "Block") {
    let result = "";
    node.children?.forEach((child) => {
      result += cssNodes(child, depth + 1);
    });
    return result;
  }
  return "";
}
export function sanitizeMailStyles(doc: Document): string {
  doc.querySelectorAll<HTMLElement>("[style]").forEach((el) => {
    const clean = declarations(el.style);
    if (clean) el.setAttribute("style", clean);
    else el.removeAttribute("style");
  });
  let result = "";
  doc.querySelectorAll("style").forEach((el) => {
    try {
      result += cssNodes(parse(el.textContent || ""));
    } catch {
      /* Discard malformed stylesheets. */
    }
    el.remove();
  });
  return result;
}
