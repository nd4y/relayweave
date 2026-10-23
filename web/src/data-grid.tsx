import {DataGrid as MuiDataGrid,type DataGridProps} from '@mui/x-data-grid';
import {ruRU} from '@mui/x-data-grid/locales';

const localeText={
 ...ruRU.components.MuiDataGrid.defaultProps.localeText,
 paginationRowsPerPage:'Строк на странице:',
 paginationDisplayedRows:({from,to,count,estimated}:{from:number;to:number;count:number;estimated?:number})=>`${from}-${to} из ${count>=0?count:estimated?`около ${estimated}`:`более ${to}`}`,
};

export function DataGrid(props:DataGridProps<any>){return <MuiDataGrid {...props} localeText={localeText}/>;}
