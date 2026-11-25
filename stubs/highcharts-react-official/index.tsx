import React from 'react';

interface HighchartsReactProps {
  containerProps?: React.HTMLAttributes<HTMLDivElement>;
}

const HighchartsReact: React.FC<HighchartsReactProps> = ({ containerProps }) => {
  return <div {...containerProps} />;
};

export default HighchartsReact;
