# 字体资源

本目录两份字体按用户要求从相邻项目 `v2way/web/src/assets/fonts/` 原样复制，未转换、裁剪或修改。它们通过 CSS `@font-face` 由应用本地提供；Vite 发布时生成资源文件并进入 Go 内嵌前端，不需要安装系统字体或访问字体 CDN。

| 文件          | 实际字体                        | SHA-256                                                          |
| ------------- | ------------------------------- | ---------------------------------------------------------------- |
| consolas.woff | Consolas Regular                | a512f7a65dffb8330b13dd53df4ab30ec73416be99ce789f55e19f1d82cd6aca |
| lxgwzk.woff2  | LXGW ZhenKai / 霞鹜臻楷 Regular | 19609aae5bb873a2b27b87cbe082576153b37ec46d5187bb7ad6ae3f74561c39 |

为保持与 v2way 一致，中文字体继续使用 CSS 家族别名 `LXGW WenKai`；这个别名不表示文件是系统霞鹜文楷。字体文件保留各自原始版权信息，不因应用代码的许可而改变。霞鹜臻楷上游与授权见 https://github.com/lxgw/LxgwZhenKai 和随附 OFL.txt。
