// Преднастройка bun test: CSS-импорты в цепочках модулей (react-pdf, katex)
// заменяются пустым модулем — иначе рантайм bun падает на .css.
import { plugin } from "bun";

plugin({
  name: "css-stub",
  setup(build) {
    build.onLoad({ filter: /\.css$/ }, () => ({ contents: "", loader: "js" }));
  },
});
