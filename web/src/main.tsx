import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import Playground from "./Playground";
import { isPlayground } from "./api";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("Dashboard root element was not found");
createRoot(root).render(
  <StrictMode>
    {isPlayground ? <Playground /> : <App />}
  </StrictMode>,
);
