// CSS Modules 的类型（Vite 原生支持 *.module.css；tsconfig 的 types 为空，这里单独声明）。
declare module "*.module.css" {
  const classes: Readonly<Record<string, string>>;
  export default classes;
}
declare module "*.css";
