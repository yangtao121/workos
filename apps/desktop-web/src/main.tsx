import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Desktop } from "./Desktop.js";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("WorkOS root element is missing");

createRoot(root).render(
  <StrictMode>
    <Desktop />
  </StrictMode>,
);
