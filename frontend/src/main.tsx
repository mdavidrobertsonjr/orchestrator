import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { WebsiteApp } from "./HostedApp";
import { AppErrorBoundary } from "./AppErrorBoundary";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AppErrorBoundary><WebsiteApp /></AppErrorBoundary>
  </StrictMode>
);
