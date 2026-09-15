import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { WebsiteApp } from "./HostedApp";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <WebsiteApp />
  </StrictMode>
);
