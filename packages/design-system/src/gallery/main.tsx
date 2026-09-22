import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "../tokens/tokens.css";
import "../styles/base.css";
import { Gallery } from "./Gallery.tsx";

const el = document.getElementById("root");
if (!el) throw new Error("#root missing");
createRoot(el).render(
  <StrictMode>
    <Gallery />
  </StrictMode>,
);
