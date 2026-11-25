declare module "css-tree" {
  export interface CssNode {
    type: string;
    name?: string;
    property?: string;
    important?: boolean;
    value?: CssNode;
    prelude?: CssNode;
    block?: CssNode;
    children?: { forEach(fn: (child: CssNode) => void): void };
  }
  export function parse(source: string): CssNode;
  export function generate(node: CssNode): string;
}
