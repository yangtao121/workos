import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  {
    ignores: [
      "**/dist/**",
      "apps/mobile-shell/android/app/src/main/assets/**",
      "apps/mobile-shell/ios/App/App/public/**",
      "**/src/gen/**",
      "coverage/**",
      "tmp/**",
      "eslint.config.mjs",
      "tools/**/*.mjs",
    ],
  },
  js.configs.recommended,
  ...tseslint.configs.strictTypeChecked,
  {
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      "@typescript-eslint/consistent-type-imports": "error",
      "@typescript-eslint/no-floating-promises": "error",
    },
  },
  {
    files: ["apps/desktop-web/public/push-worker.js"],
    extends: [tseslint.configs.disableTypeChecked],
    languageOptions: { globals: globals.serviceworker },
  },
);
