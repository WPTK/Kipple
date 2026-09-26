import { Component, type ErrorInfo, type ReactNode } from "react";
import { StatusBlock } from "@/screens/ListPane";
import { Button } from "@/ui/button";

interface State {
  failed: boolean;
}

/**
 * The last line: an error thrown while rendering shows a message with a way out instead of a blank page. "Try
 * again" re-renders in place (a passing glitch); "Go to Unread" leaves the address that may have caused it and
 * loads the app fresh.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  override state: State = { failed: false };

  static getDerivedStateFromError(): State {
    return { failed: true };
  }

  override componentDidCatch(error: unknown, info: ErrorInfo): void {
    console.error("Kipple hit an error while drawing the page", error, info.componentStack);
  }

  override render(): ReactNode {
    if (!this.state.failed) return this.props.children;
    return (
      <StatusBlock role="alert" title="Something went wrong" body="Kipple couldn't show this page. Try again, or go back to Unread.">
        <div className="flex flex-wrap justify-center gap-2">
          <Button onClick={() => this.setState({ failed: false })}>Try again</Button>
          <Button onClick={() => window.location.assign("/l/unread")}>
            Go to Unread
          </Button>
        </div>
      </StatusBlock>
    );
  }
}
