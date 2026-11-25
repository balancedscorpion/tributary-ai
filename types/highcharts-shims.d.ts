declare module 'highcharts' {
  namespace Highcharts {
    type Options = any;
    type ChartOptions = any;
    type PlotOptions = any;
    type SeriesOptionsType = any;
    type Chart = any;
    type Point = any;
    type TooltipOptions = any;
    type TooltipFormatterCallbackFunction = (this: Point) => string;
  }

  const Highcharts: {
    merge: (...args: any[]) => any;
    addEvent: (target: any, eventName: string, handler: (...args: any[]) => void) => () => void;
    [key: string]: any;
  };

  export default Highcharts;
}

declare module 'highcharts/*' {
  const initModule: (highcharts: any) => void;
  export default initModule;
}

declare module 'highcharts-react-official' {
  import * as React from 'react';

  export interface HighchartsReactProps {
    highcharts?: any;
    constructorType?: string;
    options?: any;
    containerProps?: React.HTMLAttributes<HTMLDivElement>;
    immutable?: boolean;
    updateArgs?: any[];
    callback?: (...args: any[]) => void;
  }

  const HighchartsReact: React.ForwardRefExoticComponent<
    HighchartsReactProps & React.RefAttributes<any>
  >;

  export default HighchartsReact;
}
